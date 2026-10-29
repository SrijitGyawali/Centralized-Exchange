# labs/

Small, self-contained experiments that answer one question the product
raised, without becoming part of it. Each lab has its own directory with a
program and a `NOTES.md`: **prediction, command, observation, explanation,
and the design decision it informed.**

| Lab | Question | Answer |
|---|---|---|
| [`fsync-tail/`](fsync-tail/) | Do the rare 100 ms - 1 s fsyncs come from the journal growing? | No: preallocating changes nothing. The tail belongs to the drive and the OS, so the journal format stays as it is ([notes](fsync-tail/NOTES.md)). |

```bash
go run ./labs/fsync-tail -n 10000 -rounds 2
```
