package cli

import (
	"context"
	"fmt"
	"io"
	"strings"

	"connectrpc.com/connect"
	procmeshv1 "github.com/qleelulu/procmesh/proto/procmesh/v1"
)

func runAlert(c *client, sub string, pos []string, opt options, stdout io.Writer) error {
	switch sub {
	case "list":
		if len(pos) != 0 {
			return usageError("unexpected arguments")
		}
		return alertList(c, opt, stdout)
	case "get":
		if len(pos) != 1 || pos[0] == "" {
			return usageError("alert get requires ALERT_ID")
		}
		return alertGet(c, pos[0], stdout)
	case "channel":
		if len(pos) == 0 {
			return usageError("missing alert channel subcommand")
		}
		return runAlertChannel(c, pos[0], pos[1:], opt, stdout)
	case "policy":
		if len(pos) == 0 {
			return usageError("missing alert policy subcommand")
		}
		return runAlertPolicy(c, pos[0], pos[1:], opt, stdout)
	default:
		return usageError("unknown alert command")
	}
}

func runAlertChannel(c *client, sub string, pos []string, opt options, stdout io.Writer) error {
	switch sub {
	case "list":
		if len(pos) != 0 {
			return usageError("unexpected arguments")
		}
		return alertChannelList(c, stdout)
	case "put":
		if len(pos) != 0 {
			return usageError("unexpected arguments")
		}
		return alertChannelPut(c, opt, stdout)
	case "delete":
		if len(pos) != 1 || pos[0] == "" {
			return usageError("alert channel delete requires CHANNEL_ID")
		}
		return alertChannelDelete(c, pos[0], stdout)
	default:
		return usageError("unknown alert channel command")
	}
}

func runAlertPolicy(c *client, sub string, pos []string, opt options, stdout io.Writer) error {
	switch sub {
	case "get":
		if len(pos) != 0 {
			return usageError("unexpected arguments")
		}
		return alertPolicyGet(c, stdout)
	case "put":
		if len(pos) != 0 {
			return usageError("unexpected arguments")
		}
		return alertPolicyPut(c, opt, stdout)
	default:
		return usageError("unknown alert policy command")
	}
}

func alertList(c *client, opt options, stdout io.Writer) error {
	state := strings.ToUpper(strings.TrimSpace(opt.state))
	switch state {
	case "", "FIRING", "RESOLVED":
	default:
		return usageError("alert list --state must be FIRING or RESOLVED")
	}
	resp, err := c.alert.ListAlerts(context.Background(), connect.NewRequest(&procmeshv1.ListAlertsRequest{
		State: state,
	}))
	if err != nil {
		return err
	}
	for _, e := range resp.Msg.GetEntries() {
		printAlertEntry(stdout, e)
	}
	return nil
}

func alertGet(c *client, alertID string, stdout io.Writer) error {
	resp, err := c.alert.GetAlert(context.Background(), connect.NewRequest(&procmeshv1.GetAlertRequest{
		AlertId: alertID,
	}))
	if err != nil {
		return err
	}
	printAlertEntry(stdout, resp.Msg.GetEntry())
	return nil
}

func alertChannelList(c *client, stdout io.Writer) error {
	resp, err := c.alert.ListAlertChannels(context.Background(), connect.NewRequest(&procmeshv1.ListAlertChannelsRequest{}))
	if err != nil {
		return err
	}
	for _, ch := range resp.Msg.GetChannels() {
		printAlertChannel(stdout, ch)
	}
	return nil
}

func alertChannelPut(c *client, opt options, stdout io.Writer) error {
	if opt.batchType == "" {
		return usageError("alert channel put requires --type")
	}
	if opt.name == "" {
		return usageError("alert channel put requires --name")
	}
	enabled := false
	if opt.enabledSet {
		enabled = opt.enabled
	}
	resp, err := c.alert.PutAlertChannel(context.Background(), connect.NewRequest(&procmeshv1.PutAlertChannelRequest{
		Meta:       c.meta(),
		ChannelId:  opt.id,
		Type:       opt.batchType,
		Name:       opt.name,
		Enabled:    enabled,
		ConfigJson: opt.config,
	}))
	if err != nil {
		return err
	}
	printAlertChannel(stdout, resp.Msg.GetChannel())
	return nil
}

func alertChannelDelete(c *client, channelID string, stdout io.Writer) error {
	_, err := c.alert.DeleteAlertChannel(context.Background(), connect.NewRequest(&procmeshv1.DeleteAlertChannelRequest{
		Meta:      c.meta(),
		ChannelId: channelID,
	}))
	if err != nil {
		return err
	}
	fmt.Fprintf(stdout, "channel_id=%s\n", channelID)
	return nil
}

func alertPolicyGet(c *client, stdout io.Writer) error {
	resp, err := c.alert.GetAlertPolicy(context.Background(), connect.NewRequest(&procmeshv1.GetAlertPolicyRequest{}))
	if err != nil {
		return err
	}
	printAlertPolicy(stdout, resp.Msg.GetPolicy())
	return nil
}

func alertPolicyPut(c *client, opt options, stdout io.Writer) error {
	if !opt.dedupWindowSet {
		return usageError("alert policy put requires --dedup-window-sec")
	}
	if !opt.notifyOnResolveSet {
		return usageError("alert policy put requires --notify-on-resolve")
	}
	if !opt.cpuHighSet {
		return usageError("alert policy put requires --cpu")
	}
	if !opt.memoryHighSet {
		return usageError("alert policy put requires --memory")
	}
	if !opt.diskHighSet {
		return usageError("alert policy put requires --disk")
	}
	if !opt.consecutiveSet {
		return usageError("alert policy put requires --consecutive")
	}
	if !opt.suspectTooLongSet {
		return usageError("alert policy put requires --suspect-too-long-sec")
	}
	resp, err := c.alert.PutAlertPolicy(context.Background(), connect.NewRequest(&procmeshv1.PutAlertPolicyRequest{
		Meta: c.meta(),
		Policy: &procmeshv1.AlertPolicy{
			DedupWindowSec:      opt.dedupWindowSec,
			NotifyOnResolve:     opt.notifyOnResolve,
			CpuHighPercent:      opt.cpuHigh,
			MemoryHighPercent:   opt.memoryHigh,
			DiskHighPercent:     opt.diskHigh,
			HighConsecutiveMins: opt.consecutive,
			SuspectTooLongSec:   opt.suspectTooLongSec,
		},
	}))
	if err != nil {
		return err
	}
	printAlertPolicy(stdout, resp.Msg.GetPolicy())
	return nil
}

func printAlertEntry(stdout io.Writer, e *procmeshv1.AlertEntry) {
	if e == nil {
		return
	}
	a := e.GetAlert()
	if a == nil || a.GetAlertId() == "" {
		fmt.Fprintf(stdout, "source_node=%s freshness=%s\n", e.GetSourceNode(), e.GetFreshness())
		return
	}
	fmt.Fprintf(stdout, "alert_id=%s fingerprint=%s type=%s state=%s node=%s freshness=%s\n",
		a.GetAlertId(), a.GetFingerprint(), a.GetType(), a.GetState(), a.GetNodeId(), e.GetFreshness())
}

func printAlertChannel(stdout io.Writer, ch *procmeshv1.AlertChannel) {
	if ch == nil {
		return
	}
	fmt.Fprintf(stdout, "channel_id=%s\n", ch.GetChannelId())
	fmt.Fprintf(stdout, "type=%s\n", ch.GetType())
	fmt.Fprintf(stdout, "name=%s\n", ch.GetName())
	fmt.Fprintf(stdout, "enabled=%t\n", ch.GetEnabled())
	fmt.Fprintf(stdout, "config=%s\n", ch.GetConfigJson())
}

func printAlertPolicy(stdout io.Writer, p *procmeshv1.AlertPolicy) {
	if p == nil {
		return
	}
	fmt.Fprintf(stdout, "dedup_window_sec=%d\n", p.GetDedupWindowSec())
	fmt.Fprintf(stdout, "notify_on_resolve=%t\n", p.GetNotifyOnResolve())
	fmt.Fprintf(stdout, "cpu_high_percent=%d\n", p.GetCpuHighPercent())
	fmt.Fprintf(stdout, "memory_high_percent=%d\n", p.GetMemoryHighPercent())
	fmt.Fprintf(stdout, "disk_high_percent=%d\n", p.GetDiskHighPercent())
	fmt.Fprintf(stdout, "high_consecutive_mins=%d\n", p.GetHighConsecutiveMins())
	fmt.Fprintf(stdout, "suspect_too_long_sec=%d\n", p.GetSuspectTooLongSec())
}
