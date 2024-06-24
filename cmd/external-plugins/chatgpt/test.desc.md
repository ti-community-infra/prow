## What is changed and how it works?
Issue Number: Close #xxx

What's Changed:

The size of check_leader requests is related to the number of leaders. Generally, each region will takes 7 bytes in request and 1 byte in response, when the number of leader is high, the compression saves about 60% traffic in my test.

```
Add gzip compression for check_leader requests.
```

## Related changes
- PR to update pingcap/docs/pingcap/docs-cn:
- Need to cherry-pick to the release branch

## Check List

Tests

- Unit test
- Integration test
- Manual test (add detailed scripts or steps below)
- No code

Side effects

- Performance regression
  - Consumes more CPU
  - Consumes more MEM
- Breaking backward compatibility
  
## Release note

```
Please add a release note.

Please refer to [Release Notes Language Style Guide](https://pingcap.github.io/tidb-dev-guide/contribute-to-tidb/release-notes-style-guide.html) to write a quality release note.

If you don't think this PR needs a release note then fill it with None.
If this PR will be picked to release branch, then a release note is probably required.
```
