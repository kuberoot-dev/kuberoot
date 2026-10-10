package resourcestore

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"go.etcd.io/raft/v3"
	"go.etcd.io/raft/v3/raftpb"
	"google.golang.org/protobuf/proto"
	"k8s.io/klog/v2"
)

// transport carries Raft's messages between members over HTTP: a queue
// and a sender per peer keep each peer's messages in order, and a
// snapshot goes with the state file it stands for.
type transport struct {
	n      *Node
	client *http.Client

	mu    sync.RWMutex
	peers map[uint64]*peer
}

type peer struct {
	id    uint64
	urls  []string
	queue chan *raftpb.Message
	stop  chan struct{}
}

const (
	pathMessages = "/raft/messages"
	pathSnapshot = "/raft/snapshot"
	pathMembers  = "/members"
	pathPromote  = "/leader/promote"
	pathLease    = "/leader/lease"
	headerFrom   = "X-Store-From"
	headerTo     = "X-Store-To"
	headerCID    = "X-Store-Cluster"
)

func newTransport(n *Node, clientTLS *tls.Config) *transport {
	tr := &http.Transport{TLSClientConfig: clientTLS, MaxIdleConnsPerHost: 4, IdleConnTimeout: time.Minute,
		ResponseHeaderTimeout: 10 * time.Second}
	return &transport{n: n, client: &http.Client{Transport: tr}, peers: map[uint64]*peer{}}
}

// setPeer adds a peer or changes its addresses.
func (t *transport) setPeer(id uint64, urls []string) {
	if id == t.n.id || len(urls) == 0 {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if p, ok := t.peers[id]; ok {
		p.urls = urls
		return
	}
	p := &peer{id: id, urls: urls, queue: make(chan *raftpb.Message, 4096), stop: make(chan struct{})}
	t.peers[id] = p
	go t.run(p)
}

func (t *transport) removePeer(id uint64) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if p, ok := t.peers[id]; ok {
		close(p.stop)
		delete(t.peers, id)
	}
}

func (t *transport) stop() {
	t.mu.Lock()
	defer t.mu.Unlock()
	for id, p := range t.peers {
		close(p.stop)
		delete(t.peers, id)
	}
}

func (t *transport) url(id uint64) string {
	t.mu.RLock()
	defer t.mu.RUnlock()
	if p, ok := t.peers[id]; ok {
		return p.urls[0]
	}
	return ""
}

// send queues messages for their peers; a full queue drops them, and Raft
// sends again.
func (t *transport) send(msgs []*raftpb.Message) {
	t.mu.RLock()
	defer t.mu.RUnlock()
	for _, m := range msgs {
		p, ok := t.peers[m.GetTo()]
		if !ok {
			continue
		}
		if m.GetType() == raftpb.MsgSnap {
			go t.sendSnapshot(p.urls[0], m)
			continue
		}
		select {
		case p.queue <- m:
		default:
			t.n.raft.ReportUnreachable(m.GetTo())
		}
	}
}

func (t *transport) run(p *peer) {
	var buf bytes.Buffer
	for {
		var first *raftpb.Message
		select {
		case <-p.stop:
			return
		case first = <-p.queue:
		}
		// Whatever else is queued goes in the same request.
		batch := []*raftpb.Message{first}
	more:
		for len(batch) < 256 {
			select {
			case m := <-p.queue:
				batch = append(batch, m)
			default:
				break more
			}
		}
		buf.Reset()
		for _, m := range batch {
			raw, err := proto.Marshal(m)
			if err != nil {
				continue
			}
			buf.Write(binary.AppendUvarint(nil, uint64(len(raw))))
			buf.Write(raw)
		}
		t.mu.RLock()
		url := p.urls[0]
		t.mu.RUnlock()
		if err := t.post(url+pathMessages, p.id, bytes.NewReader(buf.Bytes())); err != nil {
			klog.V(4).Infof("send to %x: %v", p.id, err)
			t.n.raft.ReportUnreachable(p.id)
		}
	}
}

var errRemoved = errors.New("etcdserver: the member has been permanently removed from the cluster")

func (t *transport) post(url string, to uint64, body io.Reader) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, body)
	if err != nil {
		return err
	}
	t.n.stamp(req, to)
	resp, err := t.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	msg, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	switch {
	case resp.StatusCode == http.StatusGone:
		t.n.removedSelf()
		return errRemoved
	case resp.StatusCode >= 300:
		return fmt.Errorf("%s: %s", resp.Status, strings.TrimSpace(string(msg)))
	}
	return nil
}

// sendSnapshot sends a snapshot's message with a copy of the state file
// as it is now: it holds at least what the snapshot does, and the member
// that receives it skips the log entries it holds already.
func (t *transport) sendSnapshot(url string, m *raftpb.Message) {
	to := m.GetTo()
	raw, err := proto.Marshal(m)
	if err != nil {
		t.n.raft.ReportSnapshot(to, raft.SnapshotFailure)
		return
	}
	pr, pw := io.Pipe()
	go func() {
		w := bufio.NewWriter(pw)
		_, _ = w.Write(binary.AppendUvarint(nil, uint64(len(raw))))
		_, _ = w.Write(raw)
		_, err := t.n.store.WriteTo(w)
		if err == nil {
			err = w.Flush()
		}
		pw.CloseWithError(err)
	}()
	if err := t.post(url+pathSnapshot, to, pr); err != nil {
		klog.Warningf("snapshot to %x: %v", to, err)
		t.n.raft.ReportSnapshot(to, raft.SnapshotFailure)
		return
	}
	klog.Infof("snapshot at %d sent to %x", m.GetSnapshot().GetMetadata().GetIndex(), to)
	t.n.raft.ReportSnapshot(to, raft.SnapshotFinish)
}

// handler serves the peers: Raft's messages and snapshots, the member
// list a joining member looks itself up in, and calls only the leader
// answers.
func (t *transport) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc(pathMessages, func(w http.ResponseWriter, r *http.Request) {
		if !t.n.admit(w, r) {
			return
		}
		br := bufio.NewReader(r.Body)
		for {
			msg, err := readMessage(br)
			if err == io.EOF {
				break
			}
			if err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			if err := t.n.raft.Step(r.Context(), msg); err != nil && !errors.Is(err, raft.ErrStopped) {
				klog.V(4).Infof("step %s from %x: %v", msg.GetType(), msg.GetFrom(), err)
			}
		}
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc(pathSnapshot, func(w http.ResponseWriter, r *http.Request) {
		if !t.n.admit(w, r) {
			return
		}
		br := bufio.NewReaderSize(r.Body, 1<<20)
		msg, err := readMessage(br)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		index := msg.GetSnapshot().GetMetadata().GetIndex()
		file := t.n.snapshotFile(index)
		if err := writeFile(file, br); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		klog.Infof("snapshot at %d received from %x", index, msg.GetFrom())
		if err := t.n.raft.Step(r.Context(), msg); err != nil {
			http.Error(w, err.Error(), http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc(pathMembers, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(peerMembers{ClusterID: t.n.clusterID, Members: t.n.store.Members()})
	})
	mux.HandleFunc(pathPromote, func(w http.ResponseWriter, r *http.Request) {
		if !t.n.admit(w, r) {
			return
		}
		id, _ := strconv.ParseUint(r.URL.Query().Get("id"), 16, 64)
		members, err := t.n.promote(r.Context(), id)
		if err != nil {
			http.Error(w, err.Error(), http.StatusConflict)
			return
		}
		_ = json.NewEncoder(w).Encode(members)
	})
	mux.HandleFunc(pathLease, func(w http.ResponseWriter, r *http.Request) {
		if !t.n.admit(w, r) {
			return
		}
		id, _ := strconv.ParseInt(r.URL.Query().Get("id"), 16, 64)
		ttl, remaining, err := t.n.leaseOnLeader(r.Context(), id, r.URL.Query().Get("renew") == "1")
		if err != nil {
			http.Error(w, err.Error(), http.StatusConflict)
			return
		}
		_ = json.NewEncoder(w).Encode(leaseAnswer{TTL: ttl, Remaining: remaining})
	})
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"health":"true"}`)
	})
	return mux
}

type peerMembers struct {
	ClusterID uint64   `json:"clusterID"`
	Members   []Member `json:"members"`
}

type leaseAnswer struct {
	TTL       int64 `json:"ttl"`
	Remaining int64 `json:"remaining"`
}

func readMessage(r *bufio.Reader) (*raftpb.Message, error) {
	n, err := binary.ReadUvarint(r)
	if err != nil {
		return nil, err
	}
	if n > 512<<20 {
		return nil, fmt.Errorf("message of %d bytes", n)
	}
	raw := make([]byte, n)
	if _, err := io.ReadFull(r, raw); err != nil {
		return nil, err
	}
	m := &raftpb.Message{}
	return m, proto.Unmarshal(raw, m)
}

func writeFile(path string, r io.Reader) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(path+".part", os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if _, err := io.Copy(f, r); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(path+".part", path)
}

// call asks the leader for something only it answers, and decodes its
// answer into out.
func (t *transport) call(ctx context.Context, leader uint64, path string, out any) error {
	url := t.url(leader)
	if url == "" {
		return errNoLeader
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url+path, nil)
	if err != nil {
		return err
	}
	t.n.stamp(req, leader)
	resp, err := t.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return errors.New(strings.TrimSpace(string(msg)))
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

// fetchMembers asks the members at urls for the store's members, for a
// member that joins: it finds its own ID there by its peer URL.
func fetchMembers(client *http.Client, urls []string) (peerMembers, error) {
	var last error
	for _, u := range urls {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, u+pathMembers, nil)
		resp, err := client.Do(req)
		if err != nil {
			cancel()
			last = err
			continue
		}
		var pm peerMembers
		err = json.NewDecoder(resp.Body).Decode(&pm)
		resp.Body.Close()
		cancel()
		if err != nil {
			last = err
			continue
		}
		return pm, nil
	}
	return peerMembers{}, fmt.Errorf("no member answered: %v", last)
}
