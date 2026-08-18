package cli

import (
	"context"
	"fmt"
	"io"
	"strings"

	"connectrpc.com/connect"
	procmeshv1 "github.com/qleelulu/procmesh/proto/procmesh/v1"
)

func runBackup(c *client, sub string, pos []string, opt options, stdout io.Writer) error {
	switch sub {
	case "create":
		if len(pos) != 0 {
			return usageError("unexpected arguments")
		}
		return backupCreate(c, opt, stdout)
	case "list":
		if len(pos) != 0 {
			return usageError("unexpected arguments")
		}
		return backupList(c, opt, stdout)
	case "get":
		if len(pos) != 1 || pos[0] == "" {
			return usageError("backup get requires SNAPSHOT_ID")
		}
		return backupGet(c, pos[0], opt, stdout)
	case "delete":
		if len(pos) != 1 || pos[0] == "" {
			return usageError("backup delete requires SNAPSHOT_ID")
		}
		return backupDelete(c, pos[0], opt, stdout)
	case "restore":
		if len(pos) != 1 || pos[0] == "" {
			return usageError("backup restore requires SNAPSHOT_ID")
		}
		return backupRestore(c, pos[0], opt, stdout)
	default:
		return usageError("unknown backup command")
	}
}

func backupCreate(c *client, opt options, stdout io.Writer) error {
	sink, err := requireBackupSink(opt.sink)
	if err != nil {
		return err
	}
	if sink == "peer" && len(opt.peerNodes) == 0 {
		return usageError("backup create --sink=peer requires --peer-node")
	}
	resp, err := c.backup.CreateBackup(context.Background(), connect.NewRequest(&procmeshv1.CreateBackupRequest{
		Meta:          c.meta(),
		Sink:          sink,
		ProcessIds:    opt.processIDs,
		TargetNodeIds: opt.peerNodes,
	}))
	if err != nil {
		return err
	}
	printBackupSnapshot(stdout, resp.Msg.GetSnapshot())
	return nil
}

func backupList(c *client, opt options, stdout io.Writer) error {
	if opt.sink != "" {
		if _, err := requireBackupSink(opt.sink); err != nil {
			return err
		}
	}
	resp, err := c.backup.ListBackups(context.Background(), connect.NewRequest(&procmeshv1.ListBackupsRequest{
		Sink:        opt.sink,
		PeerNodeIds: opt.peerNodes,
		IncludeS3:   opt.includeS3,
	}))
	if err != nil {
		return err
	}
	for _, e := range resp.Msg.GetEntries() {
		printBackupEntry(stdout, e)
	}
	return nil
}

func backupGet(c *client, snapshotID string, opt options, stdout io.Writer) error {
	if opt.sink != "" {
		if _, err := requireBackupSink(opt.sink); err != nil {
			return err
		}
	}
	resp, err := c.backup.GetBackup(context.Background(), connect.NewRequest(&procmeshv1.GetBackupRequest{
		SnapshotId:     snapshotID,
		Sink:           opt.sink,
		SourceNodeId:   backupSourceNodeID(opt),
		IncludePayload: opt.payload,
	}))
	if err != nil {
		return err
	}
	printBackupSnapshot(stdout, resp.Msg.GetSnapshot())
	if opt.payload {
		fmt.Fprintf(stdout, "payload_bytes=%d\n", len(resp.Msg.GetPayload()))
	}
	return nil
}

func backupDelete(c *client, snapshotID string, opt options, stdout io.Writer) error {
	sink, err := requireBackupSink(opt.sink)
	if err != nil {
		return err
	}
	_, err = c.backup.DeleteBackup(context.Background(), connect.NewRequest(&procmeshv1.DeleteBackupRequest{
		Meta:         c.meta(),
		SnapshotId:   snapshotID,
		Sink:         sink,
		SourceNodeId: backupSourceNodeID(opt),
	}))
	if err != nil {
		return err
	}
	fmt.Fprintf(stdout, "snapshot_id=%s\n", snapshotID)
	fmt.Fprintf(stdout, "sink=%s\n", sink)
	return nil
}

func backupRestore(c *client, snapshotID string, opt options, stdout io.Writer) error {
	sink, err := requireBackupSink(opt.sink)
	if err != nil {
		return err
	}
	if len(opt.processIDs) != 1 || opt.processIDs[0] == "" {
		return usageError("backup restore requires exactly one --process-id")
	}
	if !opt.expectedSet {
		return usageError("backup restore requires --expected-revision")
	}
	resp, err := c.backup.RestoreBackup(context.Background(), connect.NewRequest(&procmeshv1.RestoreBackupRequest{
		Meta:         c.meta(),
		SnapshotId:   snapshotID,
		Sink:         sink,
		SourceNodeId: backupSourceNodeID(opt),
		Targets: []*procmeshv1.RestoreTarget{{
			ProcessId:        opt.processIDs[0],
			ExpectedRevision: opt.expectedRevision,
		}},
	}))
	if err != nil {
		return err
	}
	for _, r := range resp.Msg.GetResults() {
		printRestoreResult(stdout, r)
	}
	return nil
}

func requireBackupSink(sink string) (string, error) {
	s := strings.ToLower(strings.TrimSpace(sink))
	switch s {
	case "fs", "s3", "peer":
		return s, nil
	case "":
		return "", usageError("backup requires --sink=fs|s3|peer")
	default:
		return "", usageError("backup --sink must be fs|s3|peer")
	}
}

// backupSourceNodeID omits the parked "s3" hop sentinel; use --include-s3 for S3 list.
func backupSourceNodeID(opt options) string {
	if strings.EqualFold(strings.TrimSpace(opt.sourceNode), "s3") {
		return ""
	}
	return opt.sourceNode
}

func printBackupEntry(stdout io.Writer, e *procmeshv1.BackupEntry) {
	if e == nil {
		return
	}
	snapID, sink, sha := "", "", ""
	if s := e.GetSnapshot(); s != nil {
		snapID = s.GetSnapshotId()
		sink = s.GetSink()
		sha = s.GetSha256()
	}
	fresh := strings.ToUpper(strings.TrimSpace(e.GetFreshness()))
	fmt.Fprintf(stdout, "%s\t%s\t%s\t%s\t%s\n", snapID, sink, e.GetSourceNode(), fresh, sha)
}

func printBackupSnapshot(stdout io.Writer, s *procmeshv1.BackupSnapshot) {
	if s == nil {
		return
	}
	fmt.Fprintf(stdout, "snapshot_id=%s\n", s.GetSnapshotId())
	fmt.Fprintf(stdout, "sink=%s\n", s.GetSink())
	fmt.Fprintf(stdout, "sha256=%s\n", s.GetSha256())
	for _, rr := range s.GetRevisionRanges() {
		if rr == nil {
			continue
		}
		fmt.Fprintf(stdout, "revision_range process=%s min=%d max=%d\n",
			rr.GetProcessId(), rr.GetMinRevision(), rr.GetMaxRevision())
	}
}

func printRestoreResult(stdout io.Writer, r *procmeshv1.RestoreProcessResult) {
	if r == nil {
		return
	}
	fmt.Fprintf(stdout, "%s\t%s\t%d\t%s\n",
		r.GetProcessId(), r.GetStatus(), r.GetNewRevision(), r.GetError())
}
