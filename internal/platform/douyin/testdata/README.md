# testdata

## push_frames.bin

Real frames received by douyin's live page on its comment WebSocket (`/webcast/im/push/v2/`), as
forwarded by the page watcher. Format: repeated little-endian `uint32` length + frame bytes.

`TestRealFramesFixture` decodes them, so the transport layers are tested as douyin actually sends
them: frame headers (`compress_type: gzip` in a header while `payloadEncoding` says `"pb"`), gzip,
and the Response / Message wrapping.

Anonymised by `TestRefreshFixture`: only frames with comments are kept, and each comment is rebuilt
from its text and numeric fields under a placeholder viewer (`观众1`, `观众2`, …). The user record
(name, id, handle, avatar) and rich-text fields that may quote it are dropped, as are all other
message types (member joins, rankings, gifts, …), which name viewers.

Refresh (macOS, needs a live room):

```bash
scripts/e2e-danmaku.sh https://live.douyin.com/<id> 90 --dump /tmp/frames.bin
go test ./internal/platform/douyin -run TestRefreshFixture -fixture-from=/tmp/frames.bin
```
