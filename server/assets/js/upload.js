(function () {
    var maxInFlight = 4;
    var maxFiles = 1000;
    var minSendGapMs = 25;
    var max429Retries = 5;
    var refreshHeader = 'X-Upload-Refresh';

    var state = {
        queue: [],
        active: 0,
        reserved: Object.create(null),
        selections: [],
        backoffUntil: 0,
        lastSendAt: 0,
        refreshInFlight: false,
        maxSize: 0,
        walkEntries: walkEntries,
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

    // one refresh in flight at a time; selections settling meanwhile stay pending and are coalesced into
    // a follow-up issued from the current DOM once the request completes, whatever replaced the page
    function maybeRefresh() {
        if (state.active > 0 || state.queue.length > 0 || state.refreshInFlight) return;
        var live = livePath();
        var hit = false;
        for (var i = 0; i < state.selections.length; i++) {
            var sel = state.selections[i];
            if (!sel.refreshPending) continue;
            if (sel.targetDir === live) hit = true;
            sel.refreshPending = false;
        }
        if (!hit) return;
        var headers = {};
        headers[refreshHeader] = live;
        state.refreshInFlight = true;
        // the toast is body-level, so the in-flight class never lands on an ancestor the stylesheet dims
        htmx.ajax('GET', '/partials/dir-contents?path=' + encodeURIComponent(live), {
            source: document.getElementById('upload-toast'),
            target: '#page-content',
            swap: 'innerHTML',
            headers: headers
        });
    }

    // htmx fires its completion events on the source element, which a history restore detaches, so the
    // flag is cleared from the request's own XHR, hooked while the source is still attached
    function trackRefresh(evt) {
        var cfg = evt.detail && evt.detail.requestConfig;
        if (!cfg || !cfg.headers || !(refreshHeader in cfg.headers) || !evt.detail.xhr) return;
        evt.detail.xhr.addEventListener('loadend', function () {
            state.refreshInFlight = false;
            maybeRefresh();
        });
    }

    // a refresh that lands after navigation must not swap the old directory back in
    function dropStaleRefresh(evt) {
        var cfg = evt.detail && evt.detail.requestConfig;
        if (!cfg || !cfg.headers || !(refreshHeader in cfg.headers)) return;
        if (cfg.headers[refreshHeader] !== livePath()) evt.detail.shouldSwap = false;
    }

    // one send per slot: lastSendAt is claimed synchronously so workers waking together cannot both pass
    function waitForSlot() {
        return new Promise(function (resolve) {
            (function check() {
                var now = Date.now();
                var readyAt = Math.max(state.backoffUntil, state.lastSendAt + minSendGapMs);
                if (readyAt > now) { setTimeout(check, readyAt - now); return; }
                state.lastSendAt = now;
                resolve();
            })();
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
            state.queue.push({ file: file, relativeDir: entries[i].relativeDir, relativePath: relativePath, dest: dest, selection: sel, attempts: 0 });
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
            run(entry).then(next);
        }
    }

    function run(entry) {
        return waitForSlot()
            .then(function () { return send(entry); })
            .then(function (result) {
                if (result.retry) return run(entry);
                var sel = entry.selection;
                delete state.reserved[entry.dest];
                if (result.ok) sel.done++;
                else sel.failed.push({ relativePath: entry.relativePath, reason: result.reason });
                settleIfDone(sel);
            });
    }

    // only 429 is retried: tollbooth answers before the handler runs, so nothing was written
    function send(entry) {
        var formData = new FormData();
        formData.append('path', joinPath(entry.selection.targetDir, entry.relativeDir) || '.');
        formData.append('file', entry.file);

        return fetch('/upload', { method: 'POST', body: formData })
            .then(function (resp) {
                if (resp.status === 429 && entry.attempts < max429Retries) {
                    entry.attempts++;
                    var delay = Math.min(250 * Math.pow(2, entry.attempts - 1), 4000);
                    state.backoffUntil = Math.max(state.backoffUntil, Date.now() + delay);
                    return { retry: true };
                }
                var isJSON = (resp.headers.get('content-type') || '').indexOf('application/json') !== -1;
                if (!isJSON) {
                    return { ok: false, reason: resp.ok ? 'unexpected response' : resp.statusText || ('HTTP ' + resp.status) };
                }
                return resp.json().then(function (data) {
                    if (resp.ok && data.uploaded) return { ok: true };
                    return { ok: false, reason: data.error || resp.statusText || ('HTTP ' + resp.status) };
                });
            })
            .catch(function () { return { ok: false, reason: 'network error' }; });
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
        // the listener lives on document and survives swaps, so an htmx response that removed the controls
        // (session expired, or a public-read visitor logged out) must not leave paste-to-upload working
        if (!document.getElementById('upload-controls')) return;
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

    // walkEntries takes its entries as a parameter so a test can pass a fake tree; every file entry seen
    // counts toward the budget whether or not file() succeeds, so unreadable trees cannot walk past the cap
    function walkEntries(entries, budget) {
        var out = { entries: [], truncated: false, errors: [] };
        var seen = 0;

        function reason(prefix, err) {
            return prefix + (err && err.name ? ': ' + err.name : '');
        }

        function walk(entry, relativeDir) {
            return new Promise(function (resolve) {
                if (out.truncated) { resolve(); return; }
                if (entry.isFile) {
                    seen++;
                    if (seen > budget) { out.truncated = true; resolve(); return; }
                    entry.file(function (file) {
                        out.entries.push({ file: file, relativeDir: relativeDir });
                        resolve();
                    }, function (err) {
                        out.errors.push({ relativePath: joinPath(relativeDir, entry.name), reason: reason('unreadable', err) });
                        resolve();
                    });
                    return;
                }
                if (!entry.isDirectory) { resolve(); return; }
                var reader = entry.createReader();
                var dir = joinPath(relativeDir, entry.name);
                function readBatch() {
                    if (out.truncated) { resolve(); return; }
                    reader.readEntries(function (batch) {
                        if (batch.length === 0) { resolve(); return; }
                        var chain = Promise.resolve();
                        for (var i = 0; i < batch.length; i++) {
                            (function (child) { chain = chain.then(function () { return walk(child, dir); }); })(batch[i]);
                        }
                        chain.then(readBatch);
                    }, function (err) {
                        out.errors.push({ relativePath: dir, reason: reason('unreadable directory', err) });
                        resolve();
                    });
                }
                readBatch();
            });
        }

        var chain = Promise.resolve();
        for (var i = 0; i < entries.length; i++) {
            (function (entry) { chain = chain.then(function () { return walk(entry, ''); }); })(entries[i]);
        }
        return chain.then(function () { return out; });
    }

    // dragged text or links produce string items only; they must not open a selection
    function hasFileItems(dt) {
        if (dt.files && dt.files.length > 0) return true;
        for (var i = 0; dt.items && i < dt.items.length; i++) {
            if (dt.items[i].kind === 'file') return true;
        }
        return false;
    }

    // entries are captured synchronously: webkitGetAsEntry returns null once the drop handler has returned
    function onDrop(e) {
        e.preventDefault();
        var listing = document.getElementById('file-listing');
        if (listing) listing.classList.remove('drag-over');
        if (!e.dataTransfer || !hasFileItems(e.dataTransfer)) return;
        var sel = newSelection();
        var items = e.dataTransfer.items;
        var entries = [];
        var errors = [];
        for (var i = 0; items && i < items.length; i++) {
            if (items[i].kind !== 'file' || typeof items[i].webkitGetAsEntry !== 'function') continue;
            var entry = items[i].webkitGetAsEntry();
            if (entry) entries.push(entry);
            else errors.push({ relativePath: 'item ' + (i + 1), reason: 'unreadable' });
        }
        if (entries.length === 0 && errors.length === 0) {
            var files = [];
            for (var j = 0; j < e.dataTransfer.files.length; j++) {
                files.push({ file: e.dataTransfer.files[j], relativeDir: '' });
            }
            enqueue(sel, files, [], false);
            return;
        }
        walkEntries(entries, maxFiles).then(function (res) {
            enqueue(sel, res.entries, errors.concat(res.errors), res.truncated);
        });
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
        bind('upload-folder-btn', 'click', function (e) {
            e.preventDefault();
            var input = document.getElementById('upload-folder-input');
            if (input) input.click();
        });
        bind('upload-folder-input', 'change', onInputChange);
        bind('upload-file-input', 'change', onInputChange);
        function onInputChange() {
            var input = this;
            if (input.files.length === 0) return;
            var sel = newSelection();
            var entries = filesFromList(input.files);
            input.value = '';
            enqueue(sel, entries, [], false);
        }

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
    document.addEventListener('htmx:historyRestore', init);
    document.addEventListener('htmx:beforeSwap', dropStaleRefresh);
    document.addEventListener('htmx:beforeRequest', trackRefresh);
})();
