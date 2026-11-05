# Independent native tool verification

This bundle preserves scripts written and executed by a fresh DeepSeek V4.1
Flash session through OpenCode on 2026-11-05. That session received source,
upstream protocol references and one normal-user disposable scratch chat. It
was not given the earlier verification scripts, results or review. Operator
feedback requested accurate scope, safe credential handling and completion of
missing proof obligations; it did not prescribe the earlier test method.

This is a separate live check, not a default CI test. The [first code review and
its fixes](../../docs/reviews/2026-11-05-deepseek-native-review.md) and the
[actual Open WebUI callable suite](../../docs/native-tool-validation.md) cover
different layers. Read `INDEPENDENT_PROOF.md` for the executed scope and limits.

## Reproduce

Use a dedicated normal test account and one newly saved chat on a disposable
per-chat desktop connection. Do not point this at a personal or persistent
workspace. Prepare `TEST_CONTEXT.json` from `TEST_CONTEXT.example.json` and a
mode-0600 `.test-token` file containing the account's existing authorized bearer.
The descriptor holds addresses and IDs; credentials belong only in the token
file. Both live files are ignored by Git. Keep tokens out of command arguments
and logs, and delete the token file after the run.

From this bundle directory, run the invocation in `INDEPENDENT_PROOF.md`.
The scripts load credentials programmatically. Reproduction needs the same
native Open WebUI/Open Terminal protocol versions and connectivity as the
recorded run. The operator may reclaim only the disposable test container
through the normal workspace lifecycle afterwards.
