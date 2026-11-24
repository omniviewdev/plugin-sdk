package exec

// RingAccount is what the replay ring of a live session counts: the bytes the function wrote and the ring took (the session's output
// offset), how many of them were overwritten before any stream read them, how many of those while no stream was attached, how many wait
// for the reader, and how many writes had to wait for it.
type RingAccount struct {
	Written, Dropped, DroppedDetached, Queued, Stalls uint64
}

// PluginRingAccount returns the counts of the replay ring of a live session of a plugin that serves targets, read in one step, and
// false for a session that is not live. It is the engine's own account, which a test holds to the host's: the frames of the stream.
func PluginRingAccount(p *Plugin, id string) (RingAccount, bool) {
	c, found := p.v2.server.engine.session(id)
	if !found {
		return RingAccount{}, false
	}
	s := c.ring.Stats()
	return RingAccount{Written: s.Written, Dropped: s.Dropped, DroppedDetached: s.DroppedDetached, Queued: s.Queued, Stalls: s.Stalls}, true
}
