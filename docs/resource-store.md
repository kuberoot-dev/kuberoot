# A store in the model of Kubernetes resources

Status: design and prototype. Nothing here is meant for production.

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
etcd) or Pebble (as CockroachDB). The prototype uses bbolt: it is etcd's, well
understood, and a single file that a snapshot copies. Keys encode `(key,
revision)` so that a range over a key prefix yields versions in order; a
separate index from key to its latest revision makes the current read a point
lookup.

### Replication

Writes go through Raft: a write is a log entry; once committed it is applied
to the engine in log order on every member, and the revision is the log
position's index in the applied sequence. Reads that must be linearisable (the
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

## Prototype

`cmd/kuberoot-store` (`pkg/resourcestore`) is a single-member store: the etcd
v3 KV (including `RangeStream`), Watch, Lease and Maintenance services over
bbolt with the version model above, no Raft yet. Leases live in memory.

kube-apiserver v1.37.1 boots on it and serves: it seeds its own objects (five
namespaces, 73 cluster roles), and creating, patching, watching and deleting a
ConfigMap, a Secret and a namespace work, with no errors in the API server's
log. Unit tests drive it through etcd's own client: the API server's
create-if-absent and update-if-unchanged transactions, paged lists at a fixed
revision, compaction and expired continuations, watches from a past revision
with previous values, lease expiry, and `RangeStream` against `Range`.

Two things the API server needs that the etcd API does not spell out:

- **A new store is at revision 1, not 0.** The API server reads revision 0 as
  "no revision" and refuses to start.
- **`RangeStream`.** The API server of 1.37 lists through etcd 3.7's
  streaming range when the store's reported version is 3.7, and only falls
  back to paged ranges when the call is unimplemented.

Raft and the resource layer come next, in that order.

## Open questions

- Compaction and the resource layer's indexes: an index entry must be
  compacted with the version it points to, or queries at old revisions read
  dangling entries.
- Decoding built-in types for indexing ties the store to the API's protobuf
  schemas and their versions; indexing only custom resources (JSON) avoids it.
- Whether the resource layer belongs in the store or in an aggregated API
  server in front of it, which would keep the store a plain etcd replacement.
