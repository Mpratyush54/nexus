package desktopbridge

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"central-memory/internal/store"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

type Postgres struct{ db store.DBTX }

func NewPostgres(db store.DBTX) *Postgres { return &Postgres{db: db} }
func (p *Postgres) Register(ctx context.Context, d Device) error {
	// Transport snapshots are temporary; canonical chat turns are retained by
	// their own policy. This owner-scoped cleanup never touches session data.
	if _, e := p.db.Exec(ctx, `DELETE FROM desktop_execution_jobs WHERE owner_id=$1 AND expires_at < now()-interval '7 days'`, d.Owner); e != nil {
		return e
	}
	b, e := json.Marshal(d)
	if e != nil {
		return e
	}
	ct, e := p.db.Exec(ctx, `INSERT INTO desktop_execution_devices(owner_id,id,key_hash,payload,seen_at) VALUES($1,$2,$3,$4,$5)
 ON CONFLICT(owner_id,id) DO UPDATE SET payload=EXCLUDED.payload,seen_at=EXCLUDED.seen_at WHERE desktop_execution_devices.key_hash=EXCLUDED.key_hash`, d.Owner, d.ID, d.KeyHash, b, d.Seen)
	if e == nil && ct.RowsAffected() != 1 {
		return ErrConflict
	}
	return e
}
func (p *Postgres) Devices(ctx context.Context, owner string) ([]Device, error) {
	rows, e := p.db.Query(ctx, `SELECT payload,key_hash FROM desktop_execution_devices WHERE owner_id=$1 ORDER BY seen_at DESC LIMIT 100`, owner)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []Device{}
	for rows.Next() {
		var b []byte
		var h string
		if e = rows.Scan(&b, &h); e != nil {
			return nil, e
		}
		var d Device
		if e = json.Unmarshal(b, &d); e != nil {
			return nil, e
		}
		d.Owner, d.KeyHash = owner, h
		out = append(out, d)
	}
	return out, rows.Err()
}
func (p *Postgres) Device(ctx context.Context, owner, id string) (Device, error) {
	var b []byte
	var h string
	e := p.db.QueryRow(ctx, `SELECT payload,key_hash FROM desktop_execution_devices WHERE owner_id=$1 AND id=$2`, owner, id).Scan(&b, &h)
	if errors.Is(e, pgx.ErrNoRows) {
		return Device{}, ErrNotFound
	}
	if e != nil {
		return Device{}, e
	}
	var d Device
	e = json.Unmarshal(b, &d)
	d.Owner, d.KeyHash = owner, h
	return d, e
}
func (p *Postgres) Create(ctx context.Context, j Job) (Job, error) {
	// Expiration is terminal. Never requeue a previously claimed request.
	_, err := p.db.Exec(ctx, `UPDATE desktop_execution_jobs SET state='expired',revision=revision+1,payload=payload || jsonb_build_object('state','expired','revision',revision+1,'updated_at',now()) WHERE owner_id=$1 AND device_id=$2 AND expires_at<=now() AND state IN ('queued','awaiting_approval','running')`, j.Owner, j.DeviceID)
	if err != nil {
		return Job{}, err
	}
	b, e := json.Marshal(j)
	if e != nil {
		return Job{}, e
	}
	// The unique partial index, not a process-local lock or a snapshot count,
	// serializes admission across replicas. Keys are scoped to the owner.
	var raw []byte
	e = p.db.QueryRow(ctx, `WITH locked AS (SELECT id FROM desktop_execution_devices WHERE owner_id=$1 AND id=$3 FOR UPDATE), admitted AS (
		SELECT 1 FROM locked WHERE (SELECT count(*) FROM desktop_execution_jobs WHERE owner_id=$1 AND device_id=$3 AND state IN ('queued','awaiting_approval','running') AND expires_at>now())<1)
 INSERT INTO desktop_execution_jobs(owner_id,id,device_id,state,revision,created_at,expires_at,payload)
 SELECT $1,$2,$3,$4,$5,$6,$7,$8 FROM admitted ON CONFLICT(owner_id,id) DO NOTHING RETURNING payload`, j.Owner, j.ID, j.DeviceID, j.State, j.Revision, j.Created, j.Expires, b).Scan(&raw)
	if errors.Is(e, pgx.ErrNoRows) {
		old, getErr := p.Get(ctx, j.Owner, j.ID)
		if getErr == nil && old.Request == j.Request {
			return old, nil
		}
		return Job{}, ErrConflict
	}
	var pgError *pgconn.PgError
	if errors.As(e, &pgError) && pgError.Code == "23505" {
		return Job{}, ErrConflict
	}
	if e != nil {
		return Job{}, e
	}
	return decodeJob(raw, j.Owner)
}
func decodeJob(b []byte, owner string) (Job, error) {
	var j Job
	e := json.Unmarshal(b, &j)
	j.Owner = owner
	return j, e
}
func (p *Postgres) Claim(ctx context.Context, owner, device string, now time.Time) (*Job, error) {
	// SKIP LOCKED and the state predicate give exactly one claimant. Expired
	// claimed jobs stay failed/expired and are never replayed on another worker.
	var b []byte
	e := p.db.QueryRow(ctx, `WITH candidate AS (SELECT owner_id,id FROM desktop_execution_jobs WHERE owner_id=$1 AND device_id=$2 AND state='queued' AND expires_at>$3 ORDER BY created_at LIMIT 1 FOR UPDATE SKIP LOCKED)
 UPDATE desktop_execution_jobs j SET state='awaiting_approval',revision=j.revision+1,
 payload=j.payload || jsonb_build_object('state','awaiting_approval','status','Awaiting approval in Nexus Desktop','revision',j.revision+1,'updated_at',$3::timestamptz)
 FROM candidate c WHERE j.owner_id=c.owner_id AND j.id=c.id RETURNING j.payload`, owner, device, now).Scan(&b)
	if errors.Is(e, pgx.ErrNoRows) {
		return nil, nil
	}
	if e != nil {
		return nil, e
	}
	j, e := decodeJob(b, owner)
	return &j, e
}
func (p *Postgres) Get(ctx context.Context, owner, id string) (Job, error) {
	var b []byte
	e := p.db.QueryRow(ctx, `SELECT payload FROM desktop_execution_jobs WHERE owner_id=$1 AND id=$2`, owner, id).Scan(&b)
	if errors.Is(e, pgx.ErrNoRows) {
		return Job{}, ErrNotFound
	}
	if e != nil {
		return Job{}, e
	}
	j, e := decodeJob(b, owner)
	return expire(j, time.Now().UTC()), e
}
func (p *Postgres) Update(ctx context.Context, owner, device, id string, u Update, now time.Time) (Job, error) {
	j, e := p.Get(ctx, owner, id)
	if e != nil {
		return j, e
	}
	if j.DeviceID != device {
		return Job{}, ErrNotFound
	}
	j, e = Apply(j, u, now)
	if e != nil {
		return j, e
	}
	b, e := json.Marshal(j)
	if e != nil {
		return j, e
	}
	ct, e := p.db.Exec(ctx, `UPDATE desktop_execution_jobs SET state=$1,revision=$2,expires_at=$3,payload=$4 WHERE owner_id=$5 AND id=$6 AND device_id=$7 AND revision=$8 AND state IN ('awaiting_approval','running') AND expires_at>$9`, j.State, j.Revision, j.Expires, b, owner, id, device, u.Revision, now)
	if e == nil && ct.RowsAffected() != 1 {
		return Job{}, ErrConflict
	}
	return j, e
}
func (p *Postgres) Cancel(ctx context.Context, owner, id string, now time.Time) (Job, error) {
	_, e := p.db.Exec(ctx, `UPDATE desktop_execution_jobs SET state='cancelled',revision=revision+1,payload=payload || jsonb_build_object('state','cancelled','revision',revision+1,'updated_at',$3::timestamptz) WHERE owner_id=$1 AND id=$2 AND state IN ('queued','awaiting_approval','running')`, owner, id, now)
	if e != nil {
		return Job{}, e
	}
	return p.Get(ctx, owner, id)
}
