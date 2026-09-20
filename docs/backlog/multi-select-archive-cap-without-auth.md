# Bound the multi-select archive on servers without a password

`handleDownloadSelected` caps an archive at `maxZipEntries` only when `--auth.public-read` is set
(`server/handlers.go`). A server started without `--auth` is equally anonymous and has no bound at all:
anyone can tick the root directory and make the server walk and stream the whole tree.

That is the behavior on master too, so nothing regressed. It was left out of the `--auth.public-read`
branch deliberately, because capping it would change a working flow for every existing `--multi`
deployment without a flag, and nothing in issue #59 required it.

Deciding it needs an answer to two things the feature branch did not have to settle:

- whether a default-off server should be bounded at all, given the convention that optional behavior
  arrives as a flag and existing deployments do not change
- if so, whether the bound is a fixed number or an option, and what a rejected selection should look
  like in the UI. Today it is `http.Error` with a plain-text body, and `selection-status.html` posts a
  plain form with no htmx attributes, so the browser navigates away and the listing and the selection
  are lost. Recovery is the Back button.

Related: the cap is an entry preflight, not a resource bound. One large file still costs its own bytes,
and `fs.ReadDir` reads a whole directory before any limit is checked.
