# labs/

Small, self-contained Go runtime experiments that teach something the
product does not need as a feature (plan page 23). Each lab is its own
directory with `main.go` or `*_test.go` plus a `NOTES.md` covering
**prediction, command, observation, explanation, and the design choice it
informed.**

Planned labs: slice aliasing, typed-nil interfaces, scheduler and
GOMAXPROCS, buffered vs unbuffered channels, escape analysis
(`-gcflags=all=-m=2`), GC and GOGC trade-offs, the memory model plus the race
detector, and Write vs Sync durability.
