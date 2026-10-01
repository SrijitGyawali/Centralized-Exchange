# evidence/

Proof that the claims in the README hold. Every file here names the git
commit or release it came from.

- `bench/`: raw `go test -bench -benchmem` output
- `load/`: load-test results with full environment description
- `profiles/`: CPU/alloc profiles and execution traces, with a note on what they showed
- `crash/`: crash-drill reports
- `demo/`: recordings and the 3-minute demo script

Never edit a result by hand. Re-run and commit the new output.
