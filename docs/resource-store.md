# A store in the model of Kubernetes resources

Status: a lab. The core is built and replicated; the resource and protocol
layers are designs. Nothing here is meant for production, and nothing uses it
by default.

## Why

A Kubernetes cluster keeps its state in etcd: a replicated, ordered key-value
store that the API server reads and writes through the etcd v3 API. kuberoot
runs etcd on highly available control planes and kine on single ones. Both
work. This lab asks a different question: what would a store look like whose
native model is the API's own — typed objects with versions, a change feed,
schemas and access rules — so that the same store serves the control plane and,
through the Kubernetes API, the applications that keep their data as resources?

The Kubernetes API is already most of a document database: objects with an
apiVersion and kind, metadata and a spec, a resourceVersion for optimistic
concurrency, watch as a change feed, schemas with validation (CRDs, OpenAPI,
CEL), labels, RBAC, owner references and garbage collection, server-side
apply. What it lacks for application data is scale per type (the API server
lists and caches whole types), queries beyond label and field selectors, and
secondary indexes.

## Scope

The store is built in layers over one core, the way record and document layers
are built over FoundationDB:

1. **Core:** an ordered, transactional key-value store with multi-version
   concurrency control, replicated by Raft. Watch is a reading of its change
   log. It speaks the etcd v3 API, the subset kube-apiserver uses, so the API
   server runs on it unchanged.
2. **Resource layer:** objects as the API server writes them, with secondary
   indexes declared per type (a field path, extracted when an object is
   written, kept in the same transaction), and queries over them.
3. **Protocol layers (demonstrations):** a document protocol (MongoDB wire),
   SQL (PostgreSQL wire, a subset), a cache protocol (RESP with TTL and pub/sub
   over watch). Each is a mapping onto the core's keys, not a store of its own.

Out of scope: a SQL optimiser, the latency of an in-memory cache with every
write replicated, being faster than the specialised store of each model. What
a single model offers is one set of access rules, one change feed, one schema
system and one backup for everything.

## What kube-apiserver needs from the store

kube-apiserver uses a small part of etcd v3:

| RPC | Use |
|---|---|
| `KV.Range` | get one key; list a prefix with a limit and continuation; count a prefix; read at a revision |
| `KV.Txn` | create if absent (`create_revision == 0`), update or delete if unchanged (`mod_revision == n`), with the current value returned when the comparison fails |
| `KV.Compact` | drop history older than a revision (every five minutes) |
| `Watch.Watch` | changes of a prefix from a revision, with the previous value; progress notifications |
| `Lease.*` | events and other objects with a time to live |
| `Maintenance.Status` | health and the database size |

Keys are `/registry/<resource>/<namespace>/<name>`; values are the objects in
protobuf or JSON, encrypted for some types. Every write gets a new cluster-wide
revision, and an object's `resourceVersion` is the revision of its last write.

## Core design

### Data model

The core keeps, per key, its versions: `(key, revision) -> {value, create
revision, lease, tombstone}`, ordered by key and then revision. The current
revision is a single counter advanced by each committed write. A read at
revision `r` sees, for each key, its newest version not after `r`. Compaction
at `r` removes, for each key, all versions before its newest version not after
`r`, and tombstones older than `r`.

The change log is the sequence of committed writes by revision. A watch from
revision `r` replays the log after `r` for its prefix, then follows new
commits. A watch from a compacted revision gets an error, as with etcd.

### Storage engine

An embedded ordered key-value engine holds the versions on disk: bbolt (as
etcd) or Pebble (as CockroachDB). The store uses bbolt: it is etcd's, well
understood, and a single file that a snapshot copies. Keys encode `(key,
revision)` so that a range over a key prefix yields versions in order; a
separate index from key to its latest revision makes the current read a point
lookup.

### Replication

Writes go through Raft: a write is a log entry; once committed it is applied
to the engine in log order on every member, which advances the revision
itself, so that it is the same on every member. Reads that must be linearisable (the
API server's quorum reads) wait for the read index, as etcd does.

Two Raft libraries were weighed:

- **etcd's `go.etcd.io/raft`:** the library etcd itself runs on, used by
  CockroachDB and others; it is only the state machine (no transport, no
  storage), so the store writes its own log storage and transport. It has
  learners, joint consensus for membership changes, read index, pre-vote and
  check-quorum. Its semantics are exactly etcd's, so the membership model
  kuberoot already uses (learner first, then promoted) carries over.
- **`hashicorp/raft`:** a complete Raft with transport and log store
  (BoltDB), used by Consul, Nomad and Vault. Simpler to start with, but its
  learners ("non-voters") and membership changes differ from etcd's, and it
  has no read index; linearisable reads go through the log or a leader lease.

The design takes `go.etcd.io/raft`: the semantics the API server is tested
against are etcd's, and the store should fail the way etcd fails.

### Transactions

The API server's transactions compare one key's `mod_revision` (or
`create_revision == 0`) and then put or delete that key, returning the current
value on failure. The core implements general etcd transactions (compare a set
of keys, then a set of operations), applied as one Raft entry, checked against
the state at apply time. Within the resource layer a transaction also updates
the object's index entries, in the same entry.

## Resource layer

An index is declared per resource type: the type's key prefix and a field
path in the stored object, for one, `spec.nodeName` of pods or a label. When
an object of the type is written, the apply step decodes it (the API server
writes JSON for custom resources; for built-in types protobuf, which the
layer decodes with the types' schema), extracts the field and writes
`index/<type>/<field>/<value>/<key>` in the same transaction. A query reads
the index range for a value and fetches the objects at one revision.

Secrets and other encrypted types are opaque to the store: the API server
encrypts them before the store sees them, and no index can look inside.

This layer is what applications would use: list pods by node without reading
every pod, or find documents by a field, through an aggregated API next to the
cluster's, with the API server's authentication and RBAC in front.

## Protocol layers

Each protocol maps onto core keys under a prefix of its own and uses the
core's transactions and watch:

- **Documents:** a collection is a prefix; a document is a key with a JSON
  value; the `_id` and declared fields are indexes as above. The wire protocol
  is MongoDB's (the approach FerretDB takes over PostgreSQL).
- **SQL:** a table is a prefix, a row a key, primary and secondary indexes as
  above; a subset of SQL without a cost-based optimiser.
- **Cache:** keys with TTLs as leases; pub/sub as watch. Writes still go
  through Raft; for keys that can lose their last writes, an asynchronous mode
  acknowledges before replication.

## The core as built

`cmd/kuberoot-store` (`pkg/resourcestore`) is a member of the store: the etcd
v3 KV (including `RangeStream`), Watch, Lease, Cluster and Maintenance
services, replicated by `go.etcd.io/raft` v3.7. It takes etcd's flags
(`--name`, `--data-dir`, `--initial-cluster`, `--initial-cluster-state`,
client and peer URLs and certificates, `--snapshot-count`), so it runs where
an etcd member runs.

### Replication

Every write (`Put`, `DeleteRange`, `Txn` with a write, `Compact`, lease
grants and revocations) is a request in one log entry. Each member applies
committed entries in log order to its state, a bbolt file with the version
model above, in one write per batch of entries; a revision is the state's own
counter, so revisions are the same on every member. A follower forwards its
proposals to the leader, as Raft does, and waits until its own state has
applied the entry before it answers.

When the leader changes, a member proposes its writes that are still waiting
once more: an entry forwarded to a leader that died may be lost with it, or
may have committed. The state remembers, for a while, which requests it
applied (by the proposing member and a request ID), so the copy that commits
second does nothing. etcd does not do this: a write caught in a change of
leader there fails with "request timed out" or "leader changed", and the API
server returns the error.

Reads are linearizable unless the client asks for a serializable read: a
member asks the leader for its read index (one request for all reads waiting
at the moment) and reads once it has applied up to that index. Learners
serve reads and writes too, which etcd refuses.

The Raft log, its hard state and the last snapshot's metadata live in
another bbolt file, synced on every round of Raft. The state file is not
synced on every apply: before a snapshot lets the log before it go, the state
is synced, so a member that crashes applies the entries after the last
snapshot again. Rounds slower than half an election timeout are logged:
they delay heartbeats, and a slow disk costs the leadership as it does with
etcd.

### Snapshots and log compaction

Every `--snapshot-count` applied entries (10 000 by default), a member records
a snapshot at its applied index and drops the log before it, keeping
`--snapshot-catchup-entries` (5 000) for members a little behind. A snapshot
is also recorded at every membership change, without dropping the log:
Raft sends a member the newest snapshot, and refuses one whose members do not
include it, which is what a learner added after the last snapshot got.

A member too far behind for the log gets the snapshot's message and, in the
same request, a copy of the leader's state file as it is when sent. That
state holds at least what the snapshot does; the member takes it in place of
its own and skips the entries it already holds. Its watches end, and their
clients watch again.

### Leases

Leases and the keys attached to them are replicated state, unlike in the
prototype. Their clock is not: as in etcd, the leader keeps it, keep-alives
on a follower go to the leader, and a member that becomes leader gives every
lease its full time to live again, so a change of leader never expires a
lease. The leader revokes an expired lease through the log; the revocation
deletes its keys at one revision on every member.

### Membership

The Cluster API is etcd's: `MemberAdd` (as a learner or a voter),
`MemberPromote`, `MemberRemove`, `MemberUpdate`, `MemberList`. A learner is
promoted once its log is within a tenth of the leader's, as etcd checks;
only the leader knows that, so another member asks the leader over the peer
connection. A new member started with `--initial-cluster-state existing`
finds its ID by its peer URL in the member list of the others. Member IDs of
a new store come from the peer URLs and the cluster token, so every member
computes the same. A removed member learns it from the log or from its peers'
refusal, and stops; its ID cannot come back.

Peers talk over HTTP or HTTPS with mutual certificates: Raft's messages in
batches, one ordered queue per peer, and snapshots apart.

`Maintenance.Snapshot` streams a copy of the state file, and
`kuberoot-store snapshot restore FILE --data-dir DIR` makes a store of one
member from it (keys, revisions and leases; not members, log or remembered
requests), as `etcdutl snapshot restore` does for etcd. The format is the
store's own: etcd and etcdutl cannot read it.

### What the API server needs that the etcd API does not spell out

- **A new store is at revision 1, not 0.** The API server reads revision 0 as
  "no revision" and refuses to start.
- **`RangeStream`.** The API server of 1.37 lists through etcd 3.7's
  streaming range when the store's reported version is 3.7, and only falls
  back to paged ranges when the call is unimplemented.
- **Progress notifications.** A watch stream's progress notification claims
  every event up to its revision was sent. The store hands every watch each
  applied batch, with or without events for it, and answers a progress
  request with the lowest revision its watches have sent.
- **Keep-alive pings.** The API server's etcd client pings idle connections;
  the store allows pings every 5 seconds, as etcd does, instead of closing
  the connection with "too many pings".

### Running it in etcd's place

An image built with `KUBEROOT_STORE_LAB=1` carries `/usr/bin/kuberoot-store`;
others do not. Booted with `kuberoot.datastore=kuberoot-store`, a node that
creates a cluster runs the store where it would run etcd: the same
certificates, ports and data directory, the same `JoinTicket` for other
control planes (a learner, promoted once caught up), `etcdmembers` to list and
remove members, the same carrying of the store to disk at installation, and
backups restored with the store's own restore. A node asked for the lab store
on an image without it creates its cluster on kine. The choice stays with the
cluster, as with etcd.

## Measurements

### Unit tests

`go test ./pkg/resourcestore/` drives members in one process through etcd's
own client (`clientv3`), over real HTTP between members: the API server's
transactions, paged lists, compaction, watches from the past, leases, and
`RangeStream`, on one member; on three, writes on a follower read at once on
every member, the loss of the leader and its return from its log, a learner
added after the log was cut that catches up from a snapshot and is promoted
through a follower, a removed member that stops and whose ID cannot be
removed again, a restart that keeps state and revisions, replicated leases
that survive the loss of the leader and expire under the new one, and a
restore from a `Maintenance.Snapshot`. Store tests apply requests directly: a
request committed twice applies once, entries the state holds are skipped,
a failed transaction writes nothing, a revoked lease deletes its keys. The
tests pass with the race detector.

### kube-apiserver on three members

In the builder pod: three members on one machine, each with its own data
directory on the builder's volume, and kube-apiserver v1.37.1 with all three
in `--etcd-servers`. It starts, seeds its objects and serves. The load is a
round of API requests every second (create, get, patch, list in pages of 50
and delete ConfigMaps, through `kubectl`) and a watch of all ConfigMaps
along, which must see every change.

| Test | Requests | Failed | What happened |
|---|---|---|---|
| leader killed (SIGKILL) at 60 s, back at 120 s | 890 in 180 s | 0 | new leader 1.6 s after the kill; the slowest request waited 1.5 s; the old leader, restarted, caught up 0.14 s after it started |
| a follower stopped for 30 s, then removed, its data deleted, added again as a learner, promoted | 1190 in 240 s | 0 | restarted follower caught up 0.15–0.26 s after it started; the new learner caught up in 0.2 s, from the log or (with `--snapshot-count 200`) from a snapshot with the state file, and was promoted at the first try |
| the same kill before writes were proposed again on a change of leader | 120 in 120 s | 1 | the write forwarded to the dead leader timed out after 7 s |

Every watch saw every change it should: as many ADDED, MODIFIED and
DELETED events as creates, patches and deletes succeeded. A request's
median latency through `kubectl`, process start included, is 43 ms, the
99th percentile 57–95 ms.

The builder's volume is shared with builds, and its fsync stalls for seconds
now and then. A stall of 4–5 s on the leader once cost it the leadership in
the middle of a test; requests waited up to 5 s and none failed. On such a
disk etcd stalls the same way.

### A cluster on the store, and conformance

Not measured yet: the stand for it (three control planes on the lab store,
`store1`–`store3`, booted from an image built with the lab) could not start,
the workshop cluster's machines having no memory free for its virtual
machines.

## Not done

- The resource layer and the protocol layers: designs only.
- Of the etcd API: nested transactions, `Hash`/`HashKV`, alarms (`Alarm`
  answers that there are none), defragmentation, authentication with users
  and roles (`Auth`), `Range` sorting and revision filters. The API server
  uses none of them.
- Entries are JSON, values included: simple to read, larger than etcd's
  protobuf.
- A snapshot sends the whole state file in one request, held by neither side
  in memory but not resumable either.
- The state file is not synced on every apply. A crash of the process loses
  nothing (the log is synced and replayed); a loss of power before the
  operating system writes the file out leaves bbolt to recover it, which it
  may not.

## Open questions

- Compaction and the resource layer's indexes: an index entry must be
  compacted with the version it points to, or queries at old revisions read
  dangling entries.
- Decoding built-in types for indexing ties the store to the API's protobuf
  schemas and their versions; indexing only custom resources (JSON) avoids it.
- Whether the resource layer belongs in the store or in an aggregated API
  server in front of it, which would keep the store a plain etcd replacement.
