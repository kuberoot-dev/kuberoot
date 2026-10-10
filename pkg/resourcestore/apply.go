package resourcestore

import (
	"encoding/json"
	"fmt"

	bolt "go.etcd.io/bbolt"
	"go.etcd.io/raft/v3/raftpb"
	"google.golang.org/protobuf/proto"
)

// request is the command of one log entry. Every member applies it to its
// state in log order, so all members hold the same revisions.
type request struct {
	// From and ID name the call that proposed it, on the member From.
	From uint64 `json:"from"`
	ID   uint64 `json:"id"`

	Txn         *TxnRequest `json:"txn,omitempty"`
	Compact     int64       `json:"compact,omitempty"`
	LeaseGrant  *Lease      `json:"grant,omitempty"`
	LeaseRevoke int64       `json:"revoke,omitempty"`
	// Attributes: a member, once started, publishes its name and client URLs.
	Attributes *Member `json:"attrs,omitempty"`
}

// applyResult is what applying a request did, for the call waiting on it.
type applyResult struct {
	Txn      TxnResult
	Revision int64
	Members  []Member
	Err      error
}

// logEntry is a committed log entry, decoded.
type logEntry struct {
	Index uint64
	Req   *request
	// Conf is a membership change; Member the member it adds or changes.
	Conf   *raftpb.ConfChange
	Member Member
}

// published is what one applied batch changed, for watches and leases.
type published struct {
	rev     int64
	events  []Event
	granted []Lease
	revoked []int64
}

// Apply applies committed entries after the state's last applied index in
// one write of the state file, and returns each entry's result. A failure
// to write the file is fatal to the member: its state would part from the
// others'.
func (s *Store) Apply(entries []logEntry) ([]applyResult, error) {
	results := make([]applyResult, len(entries))
	var pub published
	s.mu.RLock()
	err := s.db.Update(func(tx *bolt.Tx) error {
		m := tx.Bucket(metaBucket)
		applied := uint64(getInt(m, appliedKey))
		for i, e := range entries {
			if e.Index <= applied {
				// Applied before: by an earlier run, or in the snapshot this
				// state came from.
				if e.Conf != nil {
					results[i].Members = membersTx(tx)
				}
				continue
			}
			r, err := applyOne(tx, e, &pub)
			if err != nil {
				return err
			}
			results[i] = r
			applied = e.Index
		}
		pub.rev = getInt(m, currentKey)
		return putInt(m, appliedKey, int64(applied))
	})
	s.mu.RUnlock()
	if err != nil {
		return nil, err
	}
	s.watchers.publish(pub.rev, pub.events)
	if s.onLease != nil && (len(pub.granted) > 0 || len(pub.revoked) > 0) {
		s.onLease(pub.granted, pub.revoked)
	}
	return results, nil
}

// applyOne applies one entry. Errors a request can meet (a failed
// comparison is not one) go to its caller and leave the state as it was;
// only a failure of the file is returned.
func applyOne(tx *bolt.Tx, e logEntry, pub *published) (applyResult, error) {
	var res applyResult
	if e.Conf != nil {
		return res, applyConf(tx, e, &res)
	}
	r := e.Req
	if r == nil {
		return res, nil
	}
	switch {
	case r.Txn != nil:
		if err := checkRanges(tx, r.Txn); err != nil {
			res.Err = err
			return res, nil
		}
		w := newWriter(tx)
		tr, err := txn(w, r.Txn)
		if err == ErrLeaseNotFound || err == errKeyNotFound {
			res.Err = err
			return res, nil
		}
		if err != nil {
			return res, err
		}
		res.Txn, res.Revision = tr, tr.Revision
		pub.events = append(pub.events, w.events...)
	case r.Compact != 0:
		res.Err = compact(tx, r.Compact)
		if res.Err != nil && res.Err != ErrCompacted && res.Err != ErrFutureRev {
			return res, res.Err
		}
		res.Revision = getInt(tx.Bucket(metaBucket), currentKey)
	case r.LeaseGrant != nil:
		res.Err = leaseGrant(tx, *r.LeaseGrant)
		if res.Err == nil {
			pub.granted = append(pub.granted, *r.LeaseGrant)
		} else if res.Err != ErrLeaseExists {
			return res, res.Err
		}
		res.Revision = getInt(tx.Bucket(metaBucket), currentKey)
	case r.LeaseRevoke != 0:
		w := newWriter(tx)
		rev, err := leaseRevoke(w, r.LeaseRevoke)
		if err == ErrLeaseNotFound {
			res.Err = err
			return res, nil
		}
		if err != nil {
			return res, err
		}
		res.Revision = rev
		pub.events = append(pub.events, w.events...)
		pub.revoked = append(pub.revoked, r.LeaseRevoke)
	case r.Attributes != nil:
		if m, ok := memberTx(tx, r.Attributes.ID); ok {
			m.Name, m.ClientURLs = r.Attributes.Name, r.Attributes.ClientURLs
			if err := putMember(tx, m); err != nil {
				return res, err
			}
		}
		res.Members = membersTx(tx)
	}
	return res, nil
}

// checkRanges fails a transaction whose reads ask for a revision the state
// cannot read, before it writes anything.
func checkRanges(tx *bolt.Tx, r *TxnRequest) error {
	m := tx.Bucket(metaBucket)
	current, compacted := getInt(m, currentKey), getInt(m, compactKey)
	for _, ops := range [][]Op{r.Success, r.Failure} {
		for _, op := range ops {
			if !op.Range || op.Rev == 0 {
				continue
			}
			if op.Rev > current {
				return ErrFutureRev
			}
			if op.Rev < compacted {
				return ErrCompacted
			}
		}
	}
	return nil
}

// applyConf records a membership change in the state.
func applyConf(tx *bolt.Tx, e logEntry, res *applyResult) error {
	cc := e.Conf
	id := cc.GetNodeId()
	removed := tx.Bucket(removedBucket)
	switch cc.GetType() {
	case raftpb.ConfChangeAddNode, raftpb.ConfChangeAddLearnerNode:
		if removed.Get(idKey(id)) != nil {
			res.Err = fmt.Errorf("etcdserver: ID removed")
			break
		}
		m, ok := memberTx(tx, id)
		if !ok {
			m = e.Member
		}
		m.ID = id
		if len(e.Member.PeerURLs) > 0 {
			m.PeerURLs = e.Member.PeerURLs
		}
		m.IsLearner = cc.GetType() == raftpb.ConfChangeAddLearnerNode
		if err := putMember(tx, m); err != nil {
			return err
		}
	case raftpb.ConfChangeRemoveNode:
		if err := tx.Bucket(membersBucket).Delete(idKey(id)); err != nil {
			return err
		}
		if err := removed.Put(idKey(id), []byte{1}); err != nil {
			return err
		}
	case raftpb.ConfChangeUpdateNode:
		if m, ok := memberTx(tx, id); ok {
			m.PeerURLs = e.Member.PeerURLs
			if err := putMember(tx, m); err != nil {
				return err
			}
		}
	}
	res.Members = membersTx(tx)
	return nil
}

// decodeEntry reads a committed raft entry.
func decodeEntry(ent *raftpb.Entry) (logEntry, error) {
	e := logEntry{Index: ent.GetIndex()}
	switch ent.GetType() {
	case raftpb.EntryNormal:
		if len(ent.GetData()) == 0 {
			return e, nil // a new leader's empty entry
		}
		var r request
		if err := json.Unmarshal(ent.GetData(), &r); err != nil {
			return e, fmt.Errorf("entry %d: %w", e.Index, err)
		}
		e.Req = &r
	case raftpb.EntryConfChange:
		cc := &raftpb.ConfChange{}
		if err := proto.Unmarshal(ent.GetData(), cc); err != nil {
			return e, fmt.Errorf("entry %d: %w", e.Index, err)
		}
		e.Conf = cc
		if len(cc.GetContext()) > 0 {
			if err := json.Unmarshal(cc.GetContext(), &e.Member); err != nil {
				return e, fmt.Errorf("entry %d: member: %w", e.Index, err)
			}
		}
	default:
		return e, fmt.Errorf("entry %d: unsupported type %s", e.Index, ent.GetType())
	}
	return e, nil
}
