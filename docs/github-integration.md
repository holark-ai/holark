# GitHub discussions and comment CLI

Holark issue pages show a chronological GitHub discussion, with cached
comments available while refreshing. You can create, edit, delete, and quote
comments in the discussion panel. Quote replies are ordinary Markdown
comments. Markdown supports links, code, quotes, and inline images, including
GitHub HTML image tags.

## Attachments

Pull request descriptions, issues, comments, and replies accept images through
**Attach image**, drag and drop, or clipboard paste. Uploads use the active
`gh` login and require write access to the connected GitHub repository. PNG,
JPEG, GIF, WebP, and SVG images up to 10 MB are supported.
The editor previews attachments before the text is published.

Images are uploaded immediately to GitHub, and their permanent GitHub URLs are
saved as ordinary Markdown references. Holark does not store or serve the
image files. Private images are displayed using temporary URLs obtained
through GitHub authentication; the browser loads the image directly from
GitHub. Discarding a draft does not undo an upload to GitHub.

## Comment CLI

```sh
holark issue comment list ISSUE_ID
holark issue comment list ISSUE_ID --refresh --json
holark issue comment sync ISSUE_ID
holark issue comment create ISSUE_ID --body 'Additional context'
holark issue comment create ISSUE_ID --body-file comment.md
printf 'Additional context\n' | holark issue comment create ISSUE_ID --body-file -
holark issue comment edit COMMENT_ID --body-file comment.md
holark issue comment delete COMMENT_ID
```

All commands accept `--json`. Bodies must be nonblank and contain at most
65,536 Unicode characters. GitHub permissions apply, including on closed
issues. Posting permission remains unknown when GitHub's read API cannot
determine it; GitHub authorizes the actual write. Reads use the local cache;
use `--refresh` or `sync` to fetch changes made on GitHub.

If a mutation reports `issue_comment_projection_pending`, GitHub accepted the
change but Holark still needs to refresh its local copy.
`issue_comment_outcome_uncertain` means the change may have reached GitHub.
Both exit unsuccessfully in the CLI. Refresh the discussion and check GitHub
before retrying; creation is never retried automatically. The web UI retains
the draft and provides recovery controls.

## Agent context

Starting an issue agent attempts to synchronize comments before reading its
local snapshot. If synchronization fails, the prompt uses the cached
discussion with a freshness warning. The prompt includes up to 50 recent
comments within a 20,000-character discussion budget, with omissions and
truncation marked.

Custom issue templates can use `{{issue_comments}}`. The discussion is
appended when that variable is absent, without changing the saved template.
Posting a comment does not start or message an agent. Running agents can read
later comments through the CLI.
