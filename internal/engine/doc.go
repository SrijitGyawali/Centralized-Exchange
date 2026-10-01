// Package engine runs the single goroutine that owns all trading state and
// turns concurrent requests into one ordered, durable sequence of
// transitions.
//
// # The run loop (weeks 3-5)
//
//	for {
//	    batch := take commands from the bounded intake channel
//	    assign each one Seq and a server timestamp   // fairness point
//	    journal.Append(batch); journal.Sync()        // durability point
//	    for each cmd: result := state.Apply(cmd)     // pure transition
//	    send result on that request's 1-slot reply channel
//	    publish an immutable read snapshot tagged with the applied seq
//	}
//
// # Concurrency design
//
//   - Ownership replaces locking. Only this goroutine reads or writes State,
//     so State has no mutex. Other goroutines communicate with it through
//     channels and receive copies.
//   - The intake channel is bounded. When it is full, submit fails fast so
//     the API can answer 503 before the command is admitted.
//   - Reply channels have a buffer of 1, so the engine never blocks on a
//     client that has gone away (no goroutine leak, no stalled matching).
//   - A request context being canceled stops the *caller* from waiting. It
//     never cancels or rolls back an admitted command.
//   - The engine may block on storage (Sync). Apply itself never blocks.
//   - On a storage error the engine stops admitting work. It does not guess.
//
// The engine declares the small Journal interface it needs. Package journal
// provides the concrete implementation, and tests can supply a fake.
package engine
