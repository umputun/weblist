# fish completion for weblist (generated via go-flags)
complete -c weblist -a '(GO_FLAGS_COMPLETION=verbose weblist (commandline -cop) 2>/dev/null | string replace -r "\\s+# " "\t")'
