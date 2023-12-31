package ccr

// TODO: rewrite by state machine, such as first sync, full/incremental sync

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"sync"
	"time"

	"github.com/modern-go/gls"
	"github.com/selectdb/ccr_syncer/ccr/base"
	"github.com/selectdb/ccr_syncer/ccr/record"
	"github.com/selectdb/ccr_syncer/rpc"
	"github.com/selectdb/ccr_syncer/storage"
	utils "github.com/selectdb/ccr_syncer/utils"

	festruct "github.com/selectdb/ccr_syncer/rpc/kitex_gen/frontendservice"
	tstatus "github.com/selectdb/ccr_syncer/rpc/kitex_gen/status"
	ttypes "github.com/selectdb/ccr_syncer/rpc/kitex_gen/types"

	"github.com/pkg/errors"
	log "github.com/sirupsen/logrus"
	"go.uber.org/zap"

	_ "github.com/go-sql-driver/mysql"
)

const (
	SYNC_DURATION = time.Second * 5
)

type SyncType int

const (
	DBSync    SyncType = 0
	TableSync SyncType = 1
)

func (s SyncType) String() string {
	switch s {
	case DBSync:
		return "db_sync"
	case TableSync:
		return "table_sync"
	default:
		return "unknown_sync"
	}
}

type JobState int

const (
	JobRunning JobState = 0
	JobPaused  JobState = 1
)

// JobState Stringer
func (j JobState) String() string {
	switch j {
	case JobRunning:
		return "running"
	case JobPaused:
		return "paused"
	default:
		return "unknown"
	}
}

type Job struct {
	SyncType          SyncType        `json:"sync_type"`
	Name              string          `json:"name"`
	Src               base.Spec       `json:"src"`
	ISrc              base.ISpec      `json:"-"`
	srcMeta           IMeta           `json:"-"`
	Dest              base.Spec       `json:"dest"`
	IDest             base.ISpec      `json:"-"`
	destMeta          IMeta           `json:"-"`
	State             JobState        `json:"state"`
	destSrcTableIdMap map[int64]int64 `json:"-"`
	progress          *JobProgress    `json:"-"`
	db                storage.DB      `json:"-"`
	jobFactory        *JobFactory     `json:"-"`
	rpcFactory        rpc.IRpcFactory `json:"-"`
	stop              chan struct{}   `json:"-"`
	lock              sync.Mutex      `json:"-"`
}

type JobContext struct {
	context.Context
	src     base.Spec
	dest    base.Spec
	db      storage.DB
	factory *Factory
}

func NewJobContext(src, dest base.Spec, db storage.DB, factory *Factory) *JobContext {
	return &JobContext{
		Context: context.Background(),
		src:     src,
		dest:    dest,
		db:      db,
		factory: factory,
	}
}

// new job
func NewJobFromService(name string, ctx context.Context) (*Job, error) {
	jobContext, ok := ctx.(*JobContext)
	if !ok {
		return nil, errors.Errorf("invalid context type: %T", ctx)
	}
	metaFactory := jobContext.factory.MetaFactory
	iSpecFactory := jobContext.factory.ISpecFactory
	iSrc := iSpecFactory.NewISpec(&jobContext.src)
	iDest := iSpecFactory.NewISpec(&jobContext.dest)
	job := &Job{
		Name:              name,
		Src:               jobContext.src,
		ISrc:              iSrc,
		srcMeta:           metaFactory.NewMeta(&jobContext.src),
		Dest:              jobContext.dest,
		IDest:             iDest,
		destMeta:          metaFactory.NewMeta(&jobContext.dest),
		State:             JobRunning,
		destSrcTableIdMap: make(map[int64]int64),
		progress:          nil,
		db:                jobContext.db,
		stop:              make(chan struct{}),
	}

	if err := job.valid(); err != nil {
		return nil, errors.Wrap(err, "job is invalid")
	}

	if job.Src.Table == "" {
		job.SyncType = DBSync
	} else {
		job.SyncType = TableSync
	}

	job.jobFactory = NewJobFactory()
	job.rpcFactory = jobContext.factory.RpcFactory

	return job, nil
}

func NewJobFromJson(jsonData string, db storage.DB, factory *Factory) (*Job, error) {
	var job Job
	err := json.Unmarshal([]byte(jsonData), &job)
	if err != nil {
		return nil, errors.Wrapf(err, "unmarshal json failed, json: %s", jsonData)
	}
	job.ISrc = factory.ISpecFactory.NewISpec(&job.Src)
	job.IDest = factory.ISpecFactory.NewISpec(&job.Dest)
	job.srcMeta = factory.MetaFactory.NewMeta(&job.Src)
	job.destMeta = factory.MetaFactory.NewMeta(&job.Dest)
	job.destSrcTableIdMap = make(map[int64]int64)
	job.progress = nil
	job.db = db
	job.stop = make(chan struct{})
	job.jobFactory = NewJobFactory()
	job.rpcFactory = factory.RpcFactory
	return &job, nil
}

func (j *Job) valid() error {
	var err error
	if exist, err := j.db.IsJobExist(j.Name); err != nil {
		return errors.Wrap(err, "check job exist failed")
	} else if exist {
		return errors.Errorf("job %s already exist", j.Name)
	}

	if j.Name == "" {
		return errors.New("name is empty")
	}

	err = j.ISrc.Valid()
	if err != nil {
		return errors.Wrap(err, "src spec is invalid")
	}

	err = j.IDest.Valid()
	if err != nil {
		return errors.Wrap(err, "dest spec is invalid")
	}

	if (j.Src.Table == "" && j.Dest.Table != "") || (j.Src.Table != "" && j.Dest.Table == "") {
		return errors.New("src/dest are not both db or table sync")
	}

	return nil
}

func (j *Job) RecoverDatabaseSync() error {
	// TODO(Drogon): impl
	return nil
}

// database old data sync
func (j *Job) DatabaseOldDataSync() error {
	// TODO(Drogon): impl
	// Step 1: drop all tables
	err := j.IDest.ClearDB()
	if err != nil {
		return err
	}

	// Step 2: make snapshot

	return nil
}

// database sync
func (j *Job) DatabaseSync() error {
	// TODO(Drogon): impl
	return nil
}

func (j *Job) genExtraInfo() (*base.ExtraInfo, error) {
	meta := j.srcMeta
	masterToken, err := meta.GetMasterToken(j.rpcFactory)
	if err != nil {
		return nil, err
	}

	backends, err := meta.GetBackends()
	if err != nil {
		return nil, err
	}

	log.Debugf("found backends: %v", backends)

	beNetworkMap := make(map[int64]base.NetworkAddr)
	for _, backend := range backends {
		log.Infof("backend: %v", backend)
		addr := base.NetworkAddr{
			Ip:   backend.Host,
			Port: backend.HttpPort,
		}
		beNetworkMap[backend.Id] = addr
	}

	return &base.ExtraInfo{
		BeNetworkMap: beNetworkMap,
		Token:        masterToken,
	}, nil
}

func (j *Job) fullSync() error {
	type inMemoryData struct {
		SnapshotName      string                        `json:"snapshot_name"`
		SnapshotResp      *festruct.TGetSnapshotResult_ `json:"snapshot_resp"`
		TableCommitSeqMap map[int64]int64               `json:"table_commit_seq_map"`
	}

	// TODO: snapshot machine, not need create snapshot each time
	// TODO(Drogon): check last snapshot commitSeq > first commitSeq, maybe we can reuse this snapshot
	switch j.progress.SubSyncState {
	case BeginCreateSnapshot:
		// Step 1: Create snapshot
		log.Infof("fullsync status: create snapshot")

		backupTableList := make([]string, 0)
		switch j.SyncType {
		case DBSync:
			tables, err := j.srcMeta.GetTables()
			if err != nil {
				return err
			}
			for _, table := range tables {
				backupTableList = append(backupTableList, table.Name)
			}
		case TableSync:
			backupTableList = append(backupTableList, j.Src.Table)
		default:
			return errors.Errorf("invalid sync type %s", j.SyncType)
		}
		snapshotName, err := j.ISrc.CreateSnapshotAndWaitForDone(backupTableList)
		if err != nil {
			return err
		}

		j.progress.NextSubWithPersist(GetSnapshotInfo, snapshotName)

	case GetSnapshotInfo:
		// Step 2: Get snapshot info
		log.Infof("fullsync status: get snapshot info")

		snapshotName := j.progress.PersistData
		src := &j.Src
		srcRpc, err := j.rpcFactory.NewFeRpc(src)
		if err != nil {
			return err
		}

		log.Debugf("begin get snapshot %s", snapshotName)
		snapshotResp, err := srcRpc.GetSnapshot(src, snapshotName)
		if err != nil {
			return err
		}
		log.Debugf("job: %s", string(snapshotResp.GetJobInfo()))

		if !snapshotResp.IsSetJobInfo() {
			return errors.New("jobInfo is not set")
		}

		tableCommitSeqMap, err := ExtractTableCommitSeqMap(snapshotResp.GetJobInfo())
		if err != nil {
			return err
		}

		if j.SyncType == TableSync {
			if _, ok := tableCommitSeqMap[j.Src.TableId]; !ok {
				return errors.Errorf("tableid %d, commit seq not found", j.Src.TableId)
			}
		}

		inMemoryData := &inMemoryData{
			SnapshotName:      snapshotName,
			SnapshotResp:      snapshotResp,
			TableCommitSeqMap: tableCommitSeqMap,
		}
		j.progress.NextSub(AddExtraInfo, inMemoryData)

	case AddExtraInfo:
		// Step 3: Add extra info
		log.Infof("fullsync status: add extra info")

		inMemoryData := j.progress.InMemoryData.(*inMemoryData)
		snapshotResp := inMemoryData.SnapshotResp
		jobInfo := snapshotResp.GetJobInfo()
		tableCommitSeqMap := inMemoryData.TableCommitSeqMap

		var jobInfoMap map[string]interface{}
		err := json.Unmarshal(jobInfo, &jobInfoMap)
		if err != nil {
			return errors.Wrapf(err, "unmarshal jobInfo failed, jobInfo: %s", string(jobInfo))
		}
		log.Debugf("jobInfo: %v", jobInfoMap)

		extraInfo, err := j.genExtraInfo()
		if err != nil {
			return err
		}
		log.Debugf("extraInfo: %v", extraInfo)

		jobInfoMap["extra_info"] = extraInfo
		jobInfoBytes, err := json.Marshal(jobInfoMap)
		if err != nil {
			return errors.Errorf("marshal jobInfo failed, jobInfo: %v", jobInfoMap)
		}
		log.Debugf("jobInfoBytes: %s", string(jobInfoBytes))
		snapshotResp.SetJobInfo(jobInfoBytes)

		var commitSeq int64 = math.MaxInt64
		switch j.SyncType {
		case DBSync:
			for _, seq := range tableCommitSeqMap {
				commitSeq = utils.Min(commitSeq, seq)
			}
			j.progress.TableCommitSeqMap = tableCommitSeqMap // persist in CommitNext
		case TableSync:
			commitSeq = tableCommitSeqMap[j.Src.TableId]
		}
		j.progress.CommitNextSubWithPersist(commitSeq, RestoreSnapshot, inMemoryData)

	case RestoreSnapshot:
		// Step : Restore snapshot
		log.Infof("fullsync status: restore snapshot")

		if j.progress.InMemoryData == nil {
			persistData := j.progress.PersistData
			inMemoryData := &inMemoryData{}
			if err := json.Unmarshal([]byte(persistData), inMemoryData); err != nil {
				// TODO: return to snapshot
				return errors.Errorf("unmarshal persistData failed, persistData: %s", persistData)
			}
			j.progress.InMemoryData = inMemoryData
		}

		// Step 4: start a new fullsync && persist
		inMemoryData := j.progress.InMemoryData.(*inMemoryData)
		snapshotName := inMemoryData.SnapshotName
		snapshotResp := inMemoryData.SnapshotResp

		// Step 5: restore snapshot
		// Restore snapshot to dest
		dest := &j.Dest
		destRpc, err := j.rpcFactory.NewFeRpc(dest)
		if err != nil {
			return err
		}
		log.Debugf("begin restore snapshot %s", snapshotName)
		restoreResp, err := destRpc.RestoreSnapshot(dest, snapshotName, snapshotResp)
		if err != nil {
			return err
		}
		log.Infof("resp: %v", restoreResp)
		// TODO: impl wait for done, use show restore
		restoreFinished, err := j.IDest.CheckRestoreFinished(snapshotName)
		if err != nil {
			return err
		}
		if !restoreFinished {
			err = errors.Errorf("check restore state timeout, max try times: %d", base.MAX_CHECK_RETRY_TIMES)
			return err
		}
		j.progress.NextSubWithPersist(PersistRestoreInfo, snapshotName)

	case PersistRestoreInfo:
		// Step 6: Update job progress && dest table id
		// update job info, only for dest table id
		log.Infof("fullsync status: persist restore info")

		// TODO: retry && mark it for not start a new full sync
		switch j.SyncType {
		case DBSync:
			j.progress.NextWithPersist(j.progress.CommitSeq, DBTablesIncrementalSync, DB_1, "")
		case TableSync:
			if destTableId, err := j.destMeta.GetTableId(j.Dest.Table); err != nil {
				return err
			} else {
				j.Dest.TableId = destTableId
			}

			// TODO: reload check job table id
			if err := j.persistJob(); err != nil {
				return err
			}

			j.progress.TableCommitSeqMap = nil
			j.progress.NextWithPersist(j.progress.CommitSeq, TableIncrementalSync, DB_1, "")
		default:
			return errors.Errorf("invalid sync type %d", j.SyncType)
		}

		return nil
	default:
		return errors.Errorf("invalid job sub sync state %d", j.progress.SubSyncState)
	}

	return j.fullSync()
}

func (j *Job) persistJob() error {
	data, err := json.Marshal(j)
	if err != nil {
		return errors.Errorf("marshal job failed, job: %v", j)
	}

	if err := j.db.UpdateJob(j.Name, string(data)); err != nil {
		return err
	}

	return nil
}

func (j *Job) newLabel(commitSeq int64) string {
	src := &j.Src
	dest := &j.Dest
	if j.SyncType == DBSync {
		// label "ccrj:${sync_type}:${src_db_id}:${dest_db_id}:${commit_seq}"
		return fmt.Sprintf("ccrj:%s:%d:%d:%d", j.SyncType, src.DbId, dest.DbId, commitSeq)
	} else {
		// TableSync
		// label "ccrj:${sync_type}:${src_db_id}_${src_table_id}:${dest_db_id}_${dest_table_id}:${commit_seq}"
		return fmt.Sprintf("ccrj:%s:%d_%d:%d_%d:%d", j.SyncType, src.DbId, src.TableId, dest.DbId, dest.TableId, commitSeq)
	}
}

// only called by DBSync, TableSync tableId is in Src/Dest Spec
// TODO: [Performance] improve by cache
func (j *Job) getDestTableIdBySrc(srcTableId int64) (int64, error) {
	if destTableId, ok := j.destSrcTableIdMap[srcTableId]; ok {
		return destTableId, nil
	}

	srcTableName, err := j.srcMeta.GetTableNameById(srcTableId)
	if err != nil {
		return 0, err
	}

	if destTableId, err := j.destMeta.GetTableId(srcTableName); err != nil {
		return 0, err
	} else {
		j.destSrcTableIdMap[srcTableId] = destTableId
		return destTableId, nil
	}
}

func (j *Job) getDbSyncTableRecords(upsert *record.Upsert) ([]*record.TableRecord, error) {
	commitSeq := upsert.CommitSeq
	tableCommitSeqMap := j.progress.TableCommitSeqMap
	tableRecords := make([]*record.TableRecord, 0, len(upsert.TableRecords))

	for tableId, tableRecord := range upsert.TableRecords {
		// DBIncrementalSync
		if tableCommitSeqMap == nil {
			tableRecords = append(tableRecords, tableRecord)
			continue
		}

		if tableCommitSeq, ok := tableCommitSeqMap[tableId]; ok {
			if commitSeq > tableCommitSeq {
				tableRecords = append(tableRecords, tableRecord)
			}
		} else {
			// TODO: check
		}
	}

	return tableRecords, nil
}

func (j *Job) getReleatedTableRecords(upsert *record.Upsert) ([]*record.TableRecord, error) {
	var tableRecords []*record.TableRecord //, 0, len(upsert.TableRecords))

	switch j.SyncType {
	case DBSync:
		records, err := j.getDbSyncTableRecords(upsert)
		if err != nil {
			return nil, err
		}

		if len(records) == 0 {
			return nil, nil
		}
		tableRecords = records
	case TableSync:
		tableRecord, ok := upsert.TableRecords[j.Src.TableId]
		if !ok {
			return nil, errors.Errorf("table record not found, table: %s", j.Src.Table)
		}
		tableRecords = make([]*record.TableRecord, 0, 1)
		tableRecords = append(tableRecords, tableRecord)
	default:
		return nil, errors.Errorf("invalid sync type: %s", j.SyncType)
	}

	return tableRecords, nil
}

// Table ingestBinlog
// TODO: add check success, check ingestBinlog commitInfo
// TODO: rewrite by use tableId
func (j *Job) ingestBinlog(txnId int64, tableRecords []*record.TableRecord) ([]*ttypes.TTabletCommitInfo, error) {
	log.Infof("ingestBinlog, txnId: %d", txnId)

	job, err := j.jobFactory.CreateJob(NewIngestContext(txnId, tableRecords), j, "IngestBinlog")
	if err != nil {
		return nil, err
	}

	ingestBinlogJob, ok := job.(*IngestBinlogJob)
	if !ok {
		return nil, errors.Errorf("invalid job type, job: %+v", job)
	}

	job.Run()
	if err := job.Error(); err != nil {
		return nil, err
	}
	return ingestBinlogJob.CommitInfos(), nil
}

// TODO: handle error by abort txn
func (j *Job) handleUpsert(binlog *festruct.TBinlog) error {
	log.Infof("handle upsert binlog")

	data := binlog.GetData()
	upsert, err := record.NewUpsertFromJson(data)
	if err != nil {
		return err
	}
	log.Debugf("upsert: %v", upsert)

	dest := &j.Dest
	commitSeq := upsert.CommitSeq

	// Step 1: get related tableRecords
	tableRecords, err := j.getReleatedTableRecords(upsert)
	if err != nil {
		log.Errorf("get releated table records failed, err: %+v", err)
	}
	if len(tableRecords) == 0 {
		return nil
	}
	log.Debugf("tableRecords: %v", tableRecords)
	destTableIds := make([]int64, 0, len(tableRecords))
	if j.SyncType == DBSync {
		for _, tableRecord := range tableRecords {
			if destTableId, err := j.getDestTableIdBySrc(tableRecord.Id); err != nil {
				return err
			} else {
				destTableIds = append(destTableIds, destTableId)
			}
		}
	} else {
		destTableIds = append(destTableIds, j.Dest.TableId)
	}

	// Step 2: begin txn
	log.Infof("begin txn, dest: %v, commitSeq: %d", dest, commitSeq)
	destRpc, err := j.rpcFactory.NewFeRpc(dest)
	if err != nil {
		return err
	}

	label := j.newLabel(commitSeq)

	beginTxnResp, err := destRpc.BeginTransaction(dest, label, destTableIds)
	if err != nil {
		return err
	}
	log.Debugf("resp: %v", beginTxnResp)
	if beginTxnResp.GetStatus().GetStatusCode() != tstatus.TStatusCode_OK {
		return errors.Errorf("begin txn failed, status: %v", beginTxnResp.GetStatus())
	}
	txnId := beginTxnResp.GetTxnId()
	log.Debugf("TxnId: %d, DbId: %d", txnId, beginTxnResp.GetDbId())

	j.progress.BeginTransaction(txnId)

	// Step 3: ingest binlog
	var commitInfos []*ttypes.TTabletCommitInfo
	commitInfos, err = j.ingestBinlog(txnId, tableRecords)
	if err != nil {
		return err
	}
	log.Debugf("commitInfos: %v", commitInfos)

	// Step 4: commit txn
	resp, err := destRpc.CommitTransaction(dest, txnId, commitInfos)
	if err != nil {
		return err
	}
	log.Infof("commit TxnId: %d resp: %v", txnId, resp)

	if j.SyncType == DBSync && len(j.progress.TableCommitSeqMap) > 0 {
		for tableId := range upsert.TableRecords {
			tableCommitSeq, ok := j.progress.TableCommitSeqMap[tableId]
			if !ok {
				continue
			}

			if tableCommitSeq < commitSeq {
				j.progress.TableCommitSeqMap[tableId] = commitSeq
			}
			// TODO: [PERFORMANCE] remove old commit seq
		}

		j.progress.Persist()
	}

	return nil
}

// handleAddPartition
func (j *Job) handleAddPartition(binlog *festruct.TBinlog) error {
	log.Infof("handle add partition binlog")

	data := binlog.GetData()
	addPartition, err := record.NewAddPartitionFromJson(data)
	if err != nil {
		return err
	}

	destDbName := j.Dest.Database
	var destTableName string
	if j.SyncType == TableSync {
		destTableName = j.Dest.Table
	} else if j.SyncType == DBSync {
		destTableName, err = j.destMeta.GetTableNameById(addPartition.TableId)
		if err != nil {
			return err
		}
	}

	// addPartitionSql = "ALTER TABLE " + sql
	addPartitionSql := fmt.Sprintf("ALTER TABLE %s.%s %s", destDbName, destTableName, addPartition.Sql)
	log.Infof("addPartitionSql: %s", addPartitionSql)
	return j.IDest.Exec(addPartitionSql)
}

// handleDropPartition
func (j *Job) handleDropPartition(binlog *festruct.TBinlog) error {
	log.Infof("handle drop partition binlog")

	data := binlog.GetData()
	dropPartition, err := record.NewDropPartitionFromJson(data)
	if err != nil {
		return err
	}

	destDbName := j.Dest.Database
	var destTableName string
	if j.SyncType == TableSync {
		destTableName = j.Dest.Table
	} else if j.SyncType == DBSync {
		destTableName, err = j.destMeta.GetTableNameById(dropPartition.TableId)
		if err != nil {
			return err
		}
	}

	// dropPartitionSql = "ALTER TABLE " + sql
	dropPartitionSql := fmt.Sprintf("ALTER TABLE %s.%s %s", destDbName, destTableName, dropPartition.Sql)
	log.Infof("dropPartitionSql: %s", dropPartitionSql)
	return j.IDest.Exec(dropPartitionSql)
}

// handleCreateTable
func (j *Job) handleCreateTable(binlog *festruct.TBinlog) error {
	log.Infof("handle create table binlog")

	if j.SyncType != DBSync {
		return errors.Errorf("invalid sync type: %v", j.SyncType)
	}

	data := binlog.GetData()
	createTable, err := record.NewCreateTableFromJson(data)
	if err != nil {
		return err
	}

	sql := createTable.Sql
	log.Infof("createTableSql: %s", sql)
	// HACK: for drop table
	err = j.IDest.DbExec(sql)
	j.srcMeta.GetTables()
	j.destMeta.GetTables()
	return err
}

// handleDropTable
func (j *Job) handleDropTable(binlog *festruct.TBinlog) error {
	log.Infof("handle drop table binlog")

	if j.SyncType != DBSync {
		return errors.Errorf("invalid sync type: %v", j.SyncType)
	}

	data := binlog.GetData()
	dropTable, err := record.NewDropTableFromJson(data)
	if err != nil {
		return err
	}

	tableName := dropTable.TableName
	// depreated
	if tableName == "" {
		dirtySrcTables := j.srcMeta.DirtyGetTables()
		srcTable, ok := dirtySrcTables[dropTable.TableId]
		if !ok {
			return errors.Errorf("table not found, tableId: %d", dropTable.TableId)
		}

		tableName = srcTable.Name
	}

	sql := fmt.Sprintf("DROP TABLE %s FORCE", tableName)
	log.Infof("dropTableSql: %s", sql)
	err = j.IDest.DbExec(sql)
	j.srcMeta.GetTables()
	j.destMeta.GetTables()
	return err
}

func (j *Job) handleDummy(binlog *festruct.TBinlog) error {
	dummyCommitSeq := binlog.GetCommitSeq()

	log.Infof("handle dummy binlog, need full sync. SyncType: %v, seq: %v", j.SyncType, dummyCommitSeq)

	if j.SyncType == DBSync {
		j.progress.NextWithPersist(dummyCommitSeq, DBFullSync, BeginCreateSnapshot, "")
	} else {
		j.progress.NextWithPersist(dummyCommitSeq, TableFullSync, BeginCreateSnapshot, "")
	}
	return nil
}

// handleAlterJob
func (j *Job) handleAlterJob(binlog *festruct.TBinlog) error {
	log.Infof("handle alter job binlog")

	data := binlog.GetData()
	alterJob, err := record.NewAlterJobV2FromJson(data)
	if err != nil {
		return err
	}
	if alterJob.TableName == "" {
		return errors.Errorf("invalid alter job, tableName: %s", alterJob.TableName)
	}
	if !alterJob.IsFinished() {
		return nil
	}

	// HACK: busy loop for success
	// TODO: Add to state machine
	for {
		// drop table dropTableSql
		// TODO: [IMPROVEMENT] use rename table instead of drop table
		var dropTableSql string
		if j.SyncType == TableSync {
			dropTableSql = fmt.Sprintf("DROP TABLE %s FORCE", j.Dest.Table)
		} else {
			dropTableSql = fmt.Sprintf("DROP TABLE %s FORCE", alterJob.TableName)
		}
		log.Infof("dropTableSql: %s", dropTableSql)

		if err := j.destMeta.DbExec(dropTableSql); err == nil {
			break
		}
	}

	switch j.SyncType {
	case TableSync:
		j.progress.NextWithPersist(j.progress.CommitSeq, TableFullSync, BeginCreateSnapshot, "")
	case DBSync:
		j.progress.NextWithPersist(j.progress.CommitSeq, DBFullSync, BeginCreateSnapshot, "")
	default:
		return errors.Errorf("unknown table sync type: %v", j.SyncType)
	}

	return nil
}

// handleLightningSchemaChange
func (j *Job) handleLightningSchemaChange(binlog *festruct.TBinlog) error {
	log.Infof("handle lightning schema change binlog")

	data := binlog.GetData()
	lightningSchemaChange, err := record.NewModifyTableAddOrDropColumnsFromJson(data)
	if err != nil {
		return err
	}

	log.Infof("[deadlinefen] lightningSchemaChange %v", lightningSchemaChange)

	rawSql := lightningSchemaChange.RawSql
	//   "rawSql": "ALTER TABLE `default_cluster:ccr`.`test_ddl` ADD COLUMN `nid1` int(11) NULL COMMENT \"\""
	// replace `default_cluster:${Src.Database}`.`test_ddl` to `test_ddl`
	sql := strings.Replace(rawSql, fmt.Sprintf("`default_cluster:%s`.", j.Src.Database), "", 1)
	log.Infof("lightningSchemaChangeSql, rawSql: %s, sql: %s", rawSql, sql)
	return j.IDest.DbExec(sql)
}

func (j *Job) handleBinlog(binlog *festruct.TBinlog) error {
	if binlog == nil || !binlog.IsSetCommitSeq() {
		return errors.Errorf("invalid binlog: %v", binlog)
	}

	log.Infof("[deadlinefen] binlog data: %s", binlog.GetData())

	// Step 2: update job progress
	j.progress.StartHandle(binlog.GetCommitSeq())

	// TODO: use table driven
	switch binlog.GetType() {
	case festruct.TBinlogType_UPSERT:
		if err := j.handleUpsert(binlog); err != nil {
			return err
		}
	case festruct.TBinlogType_ADD_PARTITION:
		if err := j.handleAddPartition(binlog); err != nil {
			return err
		}
	case festruct.TBinlogType_CREATE_TABLE:
		if err := j.handleCreateTable(binlog); err != nil {
			return err
		}
	case festruct.TBinlogType_DROP_PARTITION:
		if err := j.handleDropPartition(binlog); err != nil {
			return err
		}
	case festruct.TBinlogType_DROP_TABLE:
		if err := j.handleDropTable(binlog); err != nil {
			return err
		}
	case festruct.TBinlogType_ALTER_JOB:
		if err := j.handleAlterJob(binlog); err != nil {
			return err
		}
	case festruct.TBinlogType_MODIFY_TABLE_ADD_OR_DROP_COLUMNS:
		if err := j.handleLightningSchemaChange(binlog); err != nil {
			return err
		}
	case festruct.TBinlogType_DUMMY:
		if err := j.handleDummy(binlog); err != nil {
			return err
		}
	case festruct.TBinlogType_ALTER_DATABASE_PROPERTY:
		// TODO(Drogon)
	case festruct.TBinlogType_MODIFY_TABLE_PROPERTY:
		// TODO(Drogon)
	case festruct.TBinlogType_BARRIER:
		// TODO(Drogon)
	default:
		return errors.Errorf("unknown binlog type: %v", binlog.GetType())
	}

	return nil
}

func (j *Job) incrementalSync() error {
	src := &j.Src

	// Step 1: get binlog
	srcRpc, err := j.rpcFactory.NewFeRpc(src)
	if err != nil {
		return nil
	}

	for {
		commitSeq := j.progress.CommitSeq
		log.Debugf("src: %s, CommitSeq: %v", src, commitSeq)

		getBinlogResp, err := srcRpc.GetBinlog(src, commitSeq)
		if err != nil {
			return nil
		}
		log.Debugf("resp: %v", getBinlogResp)

		// Step 2: check binlog status
		status := getBinlogResp.GetStatus()
		switch status.StatusCode {
		case tstatus.TStatusCode_OK:
		case tstatus.TStatusCode_BINLOG_TOO_OLD_COMMIT_SEQ:
		case tstatus.TStatusCode_BINLOG_TOO_NEW_COMMIT_SEQ:
			return nil
		case tstatus.TStatusCode_BINLOG_DISABLE:
			return errors.Errorf("binlog is disabled")
		case tstatus.TStatusCode_BINLOG_NOT_FOUND_DB:
			return errors.Errorf("can't found db")
		case tstatus.TStatusCode_BINLOG_NOT_FOUND_TABLE:
			return errors.Errorf("can't found table")
		default:
			return errors.Errorf("invalid binlog status type: %v", status.StatusCode)
		}

		// Step 3: handle binlog records if has job
		binlogs := getBinlogResp.GetBinlogs()
		if len(binlogs) == 0 {
			return errors.Errorf("no binlog, but status code is: %v", status.StatusCode)
		}

		for _, binlog := range binlogs {

			// Step 3.1: handle binlog
			if err := j.handleBinlog(binlog); err != nil {
				return err
			}

			commitSeq := binlog.GetCommitSeq()
			if j.SyncType == DBSync && j.progress.TableCommitSeqMap != nil {
				// TODO: [PERFORMANCE] use largest tableCommitSeq in memorydata to acc it
				// when all table commit seq > commitSeq, it's true
				reachSwitchToDBIncrementalSync := true
				for _, tableCommitSeq := range j.progress.TableCommitSeqMap {
					if tableCommitSeq > commitSeq {
						reachSwitchToDBIncrementalSync = false
						break
					}
				}

				if reachSwitchToDBIncrementalSync {
					j.progress.TableCommitSeqMap = nil
					j.progress.NextWithPersist(j.progress.CommitSeq, DBIncrementalSync, DB_1, "")
				}
			}
			// Step 3.2: update progress to db
			j.progress.Done()
		}
	}
}

func (j *Job) recoverJobProgress() error {
	// parse progress
	if progress, err := NewJobProgressFromJson(j.Name, j.db); err != nil {
		log.Errorf("parse job progress failed, job: %s, err: %+v", j.Name, err)
		return err
	} else {
		j.progress = progress
		return nil
	}
}

// tableSync is a function that synchronizes a table between the source and destination databases.
// If it is the first synchronization, it performs a full sync of the table.
// If it is not the first synchronization, it recovers the job progress and performs an incremental sync.
func (j *Job) tableSync() error {
	switch j.progress.SyncState {
	case TableFullSync:
		log.Debugf("table full sync")
		return j.fullSync()
	case TableIncrementalSync:
		log.Debugf("table incremental sync")
		return j.incrementalSync()
	default:
		return errors.Errorf("unknown sync state: %v", j.progress.SyncState)
	}
}

// TODO(Drogon): impl
func (j *Job) dbTablesIncrementalSync() error {
	log.Debugf("db tables incremental sync")

	return j.incrementalSync()
}

// TODO(Drogon): impl DBSpecificTableFullSync
func (j *Job) dbSpecificTableFullSync() error {
	log.Debugf("db specific table full sync")

	return nil
}

func (j *Job) dbSync() error {
	switch j.progress.SyncState {
	case DBFullSync:
		log.Debugf("db full sync")
		return j.fullSync()
	case DBTablesIncrementalSync:
		return j.dbTablesIncrementalSync()
	case DBSpecificTableFullSync:
		return j.dbSpecificTableFullSync()
	case DBIncrementalSync:
		log.Debugf("db incremental sync")
		return j.incrementalSync()
	default:
		return errors.Errorf("unknown db sync state: %v", j.progress.SyncState)
	}
}

func (j *Job) sync() error {
	j.lock.Lock()
	defer j.lock.Unlock()

	if j.State != JobRunning {
		return nil
	}

	switch j.SyncType {
	case TableSync:
		return j.tableSync()
	case DBSync:
		return j.dbSync()
	default:
		return errors.Errorf("unknown table sync type: %v", j.SyncType)
	}
}

func (j *Job) run() error {
	ticker := time.NewTicker(SYNC_DURATION)
	defer ticker.Stop()

	for {
		select {
		case <-j.stop:
			gls.DeleteGls(gls.GoID())
			log.Infof("job stopped, job: %s", j.Name)
			return nil
		case <-ticker.C:
			if err := j.sync(); err != nil {
				log.Errorf("job sync failed, job: %s, err: %+v", j.Name, err)
			}
		}
	}
}

// run job
func (j *Job) Run() error {
	gls.ResetGls(gls.GoID(), map[interface{}]interface{}{})
	gls.Set("job", j.Name)

	// retry 3 times to check IsProgressExist
	var isProgressExist bool
	var err error
	for i := 0; i < 3; i++ {
		isProgressExist, err = j.db.IsProgressExist(j.Name)
		if err != nil {
			log.Errorf("check progress exist failed, error: %+v", err)
			continue
		}
		break
	}
	if err != nil {
		return err
	}

	if isProgressExist {
		if err := j.recoverJobProgress(); err != nil {
			log.Errorf("recover job %s progress failed: %+v", j.Name, err)
			return err
		}
	} else {
		j.progress = NewJobProgress(j.Name, j.SyncType, j.db)
		switch j.SyncType {
		case TableSync:
			j.progress.NextWithPersist(0, TableFullSync, BeginCreateSnapshot, "")
		case DBSync:
			j.progress.NextWithPersist(0, DBFullSync, BeginCreateSnapshot, "")
		default:
			return errors.Errorf("unknown table sync type: %v", j.SyncType)
		}
	}

	// Hack: for drop table
	if j.SyncType == DBSync {
		j.srcMeta.GetTables()
		j.destMeta.GetTables()
	}

	return j.run()
}

// stop job
func (j *Job) Stop() {
	close(j.stop)
}

func (j *Job) FirstRun() error {
	log.Info("first run check job", zap.String("src", j.Src.String()), zap.String("dest", j.Dest.String()))

	// Step 1: check fe and be binlog feature is enabled
	if err := j.srcMeta.CheckBinlogFeature(); err != nil {
		return err
	}
	if err := j.destMeta.CheckBinlogFeature(); err != nil {
		return err
	}

	// Step 2: check src database
	if src_db_exists, err := j.ISrc.CheckDatabaseExists(); err != nil {
		return err
	} else if !src_db_exists {
		return errors.Errorf("src database %s not exists", j.Src.Database)
	}
	if j.SyncType == DBSync {
		if enable, err := j.ISrc.IsDatabaseEnableBinlog(); err != nil {
			return err
		} else if !enable {
			return errors.Errorf("src database %s not enable binlog", j.Src.Database)
		}
	}
	if srcDbId, err := j.srcMeta.GetDbId(); err != nil {
		return err
	} else {
		j.Src.DbId = srcDbId
	}

	// Step 3: check src table exists, if not exists, return err
	if j.SyncType == TableSync {
		if src_table_exists, err := j.ISrc.CheckTableExists(); err != nil {
			return err
		} else if !src_table_exists {
			return errors.Errorf("src table %s.%s not exists", j.Src.Database, j.Src.Table)
		}

		if enable, err := j.ISrc.IsTableEnableBinlog(); err != nil {
			return err
		} else if !enable {
			return errors.Errorf("src table %s.%s not enable binlog", j.Src.Database, j.Src.Table)
		}

		if srcTableId, err := j.srcMeta.GetTableId(j.Src.Table); err != nil {
			return err
		} else {
			j.Src.TableId = srcTableId
		}
	}

	// Step 4: check dest database && table exists
	// if dest database && table exists, return err
	dest_db_exists, err := j.IDest.CheckDatabaseExists()
	if err != nil {
		return err
	}
	if !dest_db_exists {
		if err := j.IDest.CreateDatabase(); err != nil {
			return err
		}
	}
	if destDbId, err := j.destMeta.GetDbId(); err != nil {
		return err
	} else {
		j.Dest.DbId = destDbId
	}
	if j.SyncType == TableSync {
		dest_table_exists, err := j.IDest.CheckTableExists()
		if err != nil {
			return err
		}
		if dest_table_exists {
			return errors.Errorf("dest table %s.%s already exists", j.Dest.Database, j.Dest.Table)
		}
	}

	return nil
}

// HACK: temp impl
func (j *Job) GetLag() (int64, error) {
	j.lock.Lock()
	defer j.lock.Unlock()

	srcSpec := &j.Src
	rpc, err := j.rpcFactory.NewFeRpc(srcSpec)
	if err != nil {
		return 0, err
	}

	commitSeq := j.progress.CommitSeq
	resp, err := rpc.GetBinlogLag(srcSpec, commitSeq)
	if err != nil {
		return 0, err
	}

	log.Debugf("resp: %v, lag: %d", resp, resp.GetLag())
	return resp.GetLag(), nil
}

func (j *Job) changeJobState(state JobState) error {
	j.lock.Lock()
	defer j.lock.Unlock()

	if j.State == state {
		log.Debugf("job %s state is already %s", j.Name, state)
		return nil
	}

	originState := j.State
	j.State = state
	if err := j.persistJob(); err != nil {
		j.State = originState
		return err
	}
	log.Debugf("change job %s state from %s to %s", j.Name, originState, state)
	return nil
}

func (j *Job) Pause() error {
	log.Infof("pause job %s", j.Name)

	return j.changeJobState(JobPaused)
}

func (j *Job) Resume() error {
	log.Infof("resume job %s", j.Name)

	return j.changeJobState(JobRunning)
}

type JobStatus struct {
	Name          string `json:"name"`
	State         string `json:"state"`
	ProgressState string `json:"progress_state"`
}

func (j *Job) Status() *JobStatus {
	j.lock.Lock()
	defer j.lock.Unlock()

	state := j.State.String()
	progress_state := j.progress.SyncState.String()

	return &JobStatus{
		Name:          j.Name,
		State:         state,
		ProgressState: progress_state,
	}
}
