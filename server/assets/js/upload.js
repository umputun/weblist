(function () {
    var maxInFlight = 4;
    var maxFiles = 1000;

    var state = {
        queue: [],
        active: 0,
        reserved: Object.create(null),
        selections: [],
        maxSize: 0,
        enqueue: enqueue
    };
    window.weblistUpload = state;

    function livePath() {
        var controls = document.getElementById('upload-controls');
        return controls ? controls.getAttribute('data-path') : '.';
    }

    function joinPath() {
        var parts = [];
        for (var i = 0; i < arguments.length; i++) {
            var segs = String(arguments[i] || '').split('/');
            for (var j = 0; j < segs.length; j++) {
                if (segs[j] !== '' && segs[j] !== '.') parts.push(segs[j]);
            }
        }
        return parts.join('/');
    }

    function showToast(msg, isError) {
        var toast = document.getElementById('upload-toast');
        if (!toast) return;
        toast.textContent = msg;
        toast.className = 'upload-toast active' + (isError ? ' error' : ' success');
        clearTimeout(toast._hideTimer);
        toast._hideTimer = setTimeout(function () { toast.classList.remove('active'); }, 4000);
    }

    function newSelection() {
        var sel = { targetDir: livePath(), total: 0, done: 0, failed: [], skipped: [], refreshPending: false };
        state.selections.push(sel);
        return sel;
    }

    function summarize(sel) {
        var n = sel.done;
        var msg = 'Uploaded ' + n + (n === 1 ? ' file' : ' files');
        if (sel.failed.length > 0) {
            var items = [];
            for (var i = 0; i < sel.failed.length; i++) {
                items.push(sel.failed[i].relativePath + ' (' + sel.failed[i].reason + ')');
            }
            msg += ', ' + sel.failed.length + ' failed: ' + items.join(', ');
        }
        if (sel.skipped.length > 0) {
            msg += ', ' + sel.skipped.length + ' skipped as duplicates';
        }
        showToast(msg, sel.failed.length > 0);
    }

    function settleIfDone(sel) {
        if (sel.done + sel.failed.length + sel.skipped.length < sel.total) return;
        summarize(sel);
        sel.refreshPending = true;
        maybeRefresh();
    }

    function maybeRefresh() {
        if (state.active > 0 || state.queue.length > 0) return;
        var live = livePath();
        var hit = false;
        for (var i = 0; i < state.selections.length; i++) {
            var sel = state.selections[i];
            if (!sel.refreshPending) continue;
            if (sel.targetDir === live) hit = true;
            sel.refreshPending = false;
        }
        if (!hit) return;
        htmx.ajax('GET', '/partials/dir-contents?path=' + encodeURIComponent(live), {
            target: '#page-content',
            swap: 'innerHTML'
        });
    }


    // enqueue turns a collected selection into queued uploads; errors are discovery failures by path
    function enqueue(sel, entries, errors, truncated) {
        errors = errors || [];
        if (truncated || entries.length > maxFiles) {
            showToast('More than ' + maxFiles + ' files; the limit is ' + maxFiles, true);
            return;
        }
        sel.total = entries.length + errors.length;
        var queued = 0;
        for (var e = 0; e < errors.length; e++) sel.failed.push(errors[e]);
        for (var i = 0; i < entries.length; i++) {
            var file = entries[i].file;
            var relativePath = joinPath(entries[i].relativeDir, file.name);
            if (file.size > state.maxSize) {
                sel.failed.push({ relativePath: relativePath, reason: 'exceeds maximum size' });
                continue;
            }
            var dest = joinPath(sel.targetDir, relativePath);
            if (state.reserved[dest]) {
                sel.skipped.push(relativePath);
                continue;
            }
            state.reserved[dest] = true;
            state.queue.push({ file: file, relativeDir: entries[i].relativeDir, relativePath: relativePath, dest: dest, selection: sel });
            queued++;
        }
        if (queued === 0) { settleIfDone(sel); return; }
        while (state.active < maxInFlight && state.queue.length > 0) startWorker();
    }

    function startWorker() {
        state.active++;
        next();

        function next() {
            var entry = state.queue.shift();
            if (!entry) { state.active--; maybeRefresh(); return; }
            send(entry).then(next);
        }
    }

    function send(entry) {
        var sel = entry.selection;
        var formData = new FormData();
        formData.append('path', joinPath(sel.targetDir, entry.relativeDir) || '.');
        formData.append('file', entry.file);

        return fetch('/upload', { method: 'POST', body: formData })
            .then(function (resp) {
                var isJSON = (resp.headers.get('content-type') || '').indexOf('application/json') !== -1;
                if (!isJSON) {
                    return { ok: false, reason: resp.ok ? 'unexpected response' : resp.statusText || ('HTTP ' + resp.status) };
                }
                return resp.json().then(function (data) {
                    if (resp.ok && data.uploaded) return { ok: true };
                    return { ok: false, reason: data.error || resp.statusText || ('HTTP ' + resp.status) };
                });
            })
            .catch(function () { return { ok: false, reason: 'network error' }; })
            .then(function (result) {
                delete state.reserved[entry.dest];
                if (result.ok) sel.done++;
                else sel.failed.push({ relativePath: entry.relativePath, reason: result.reason });
                settleIfDone(sel);
            });
    }

    function filesFromList(list) {
        var entries = [];
        for (var i = 0; i < list.length; i++) {
            var rel = list[i].webkitRelativePath || '';
            var dir = rel.indexOf('/') !== -1 ? rel.substring(0, rel.lastIndexOf('/')) : '';
            entries.push({ file: list[i], relativeDir: dir });
        }
        return entries;
    }

    function onPaste(e) {
        if (!e.clipboardData || !e.clipboardData.files || e.clipboardData.files.length === 0) return;
        e.preventDefault();
        // browsers name pasted files generically ("image.png"), so a timestamp keeps them from colliding
        var now = new Date();
        var ts = now.getFullYear().toString() +
            ('0' + (now.getMonth() + 1)).slice(-2) +
            ('0' + now.getDate()).slice(-2) + '-' +
            ('0' + now.getHours()).slice(-2) +
            ('0' + now.getMinutes()).slice(-2) +
            ('0' + now.getSeconds()).slice(-2);
        var entries = [];
        for (var i = 0; i < e.clipboardData.files.length; i++) {
            var f = e.clipboardData.files[i];
            var ext = f.name.indexOf('.') !== -1 ? f.name.substring(f.name.lastIndexOf('.')) : '';
            var newName = 'paste-' + ts + (e.clipboardData.files.length > 1 ? '-' + (i + 1) : '') + ext;
            entries.push({ file: new File([f], newName, { type: f.type }), relativeDir: '' });
        }
        enqueue(newSelection(), entries, [], false);
    }

    function onDrop(e) {
        e.preventDefault();
        var listing = document.getElementById('file-listing');
        if (listing) listing.classList.remove('drag-over');
        if (!e.dataTransfer) return;
        var sel = newSelection();
        var entries = [];
        for (var i = 0; i < e.dataTransfer.files.length; i++) {
            entries.push({ file: e.dataTransfer.files[i], relativeDir: '' });
        }
        enqueue(sel, entries, [], false);
    }

    function bind(id, event, handler) {
        var el = document.getElementById(id);
        if (!el) return;
        if (el._weblistHandler) el.removeEventListener(event, el._weblistHandler);
        el._weblistHandler = handler;
        el.addEventListener(event, handler);
    }

    function init() {
        var controls = document.getElementById('upload-controls');
        if (!controls) return;
        state.maxSize = parseInt(controls.getAttribute('data-max-size'), 10) || 0;

        bind('upload-btn', 'click', function (e) {
            e.preventDefault();
            var input = document.getElementById('upload-file-input');
            if (input) input.click();
        });
        bind('upload-file-input', 'change', function () {
            var input = this;
            if (input.files.length === 0) return;
            var sel = newSelection();
            var entries = filesFromList(input.files);
            input.value = '';
            enqueue(sel, entries, [], false);
        });

        var listing = document.getElementById('file-listing');
        if (listing) {
            bind('file-listing', 'drop', onDrop);
            listing.addEventListener('dragover', function (e) { e.preventDefault(); listing.classList.add('drag-over'); });
            listing.addEventListener('dragleave', function (e) { e.preventDefault(); listing.classList.remove('drag-over'); });
        }
    }

    // the script loads from <head>, so listeners go on document; htmx events bubble there
    document.addEventListener('paste', onPaste);
    document.addEventListener('DOMContentLoaded', init);
    document.addEventListener('htmx:afterSwap', function (evt) {
        if (evt.detail.target && evt.detail.target.id === 'page-content') init();
    });
})();
