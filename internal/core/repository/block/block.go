package block

import (
	"context"
	"database/sql"

	"github.com/pkg/errors"
	"github.com/uptrace/bun"
	"github.com/uptrace/go-clickhouse/ch"

	"github.com/iam047801/tonidx/internal/core"
)

var _ core.BlockRepository = (*Repository)(nil)

type Repository struct {
	ch *ch.DB
	pg *bun.DB
}

func NewRepository(_ch *ch.DB, _pg *bun.DB) *Repository {
	return &Repository{ch: _ch, pg: _pg}
}

func createIndexes(ctx context.Context, pgDB *bun.DB) error {
	_, err := pgDB.NewCreateIndex().
		Model(&core.Block{}).
		Using("HASH").
		Column("workchain").
		Exec(ctx)
	if err != nil {
		return errors.Wrap(err, "block workchain pg create index")
	}

	return nil
}

func CreateTables(ctx context.Context, chDB *ch.DB, pgDB *bun.DB) error {
	_, err := chDB.NewCreateTable().
		IfNotExists().
		Engine("ReplacingMergeTree").
		Model(&core.Block{}).
		Exec(ctx)
	if err != nil {
		return errors.Wrap(err, "block ch create table")
	}

	_, err = pgDB.NewCreateTable().
		Model(&core.Block{}).
		IfNotExists().
		Exec(ctx)
	if err != nil {
		return errors.Wrap(err, "block pg create table")
	}

	return createIndexes(ctx, pgDB)
}

func (r *Repository) GetLastMasterBlock(ctx context.Context) (*core.Block, error) {
	ret := new(core.Block)

	err := r.ch.NewSelect().Model(ret).
		Where("workchain = ?", -1).
		Order("seq_no DESC").
		Limit(1).
		Scan(ctx)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, core.ErrNotFound
	}
	if err != nil {
		return nil, err
	}

	return ret, nil
}

func (r *Repository) AddBlocks(ctx context.Context, tx bun.Tx, info []*core.Block) error {
	if len(info) == 0 {
		return nil
	}
	_, err := r.ch.NewInsert().Model(&info).Exec(ctx)
	if err != nil {
		return err
	}
	_, err = tx.NewInsert().Model(&info).Exec(ctx)
	if err != nil {
		return err
	}
	return nil
}

func transactionsLoad(q *bun.SelectQuery, path string, withMessages bool) *bun.SelectQuery {
	q = q.Relation(path + "Transactions")

	if withMessages {
		q = q.Relation(path+"Transactions.InMsg").
			Relation(path+"Transactions.OutMsg", func(q *bun.SelectQuery) *bun.SelectQuery {
				return q.Where("incoming = ?", false)
			})
	}

	return q
}

func blocksFilter(q *bun.SelectQuery, f *core.BlockFilter) *bun.SelectQuery {
	if f.WithMaster {
		q = q.Relation("Master")
	}
	if f.WithShards {
		q = q.Relation("Shards")
	}
	if f.WithTransactions {
		q = transactionsLoad(q, "", f.WithTransactionMessages)
		if f.WithShards {
			q = transactionsLoad(q, "Shards.", f.WithTransactionMessages)
		}
	}

	if f.ID != nil {
		q = q.Where("workchain = ?", f.ID.Workchain).
			Where("shard = ?", f.ID.Shard).
			Where("seq_no = ?", f.ID.SeqNo)
	} else if f.Workchain != nil {
		q = q.Where("workchain = ?", *f.Workchain)
	}

	if len(f.FileHash) > 0 {
		q = q.Where("file_hash = ?", f.FileHash)
	}

	q = q.Order("seq_no DESC")

	return q
}

func (r *Repository) GetBlocks(ctx context.Context, f *core.BlockFilter, offset, limit int) (ret []*core.Block, err error) {
	err = blocksFilter(r.pg.NewSelect().Model(&ret), f).
		Offset(offset).Limit(limit).Scan(ctx)
	return ret, err
}
