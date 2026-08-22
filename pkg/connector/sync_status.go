// corten-matrix - A Matrix-iMessage puppeting bridge.
// Copyright (C) 2024 Ludvig Rhodin
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package connector

import (
	"context"

	"go.mau.fi/util/dbutil"
	"maunium.net/go/mautrix/bridgev2/commands"

	"github.com/lrhodin/corten-matrix/pkg/syncstatus"
)

// The report/query implementation lives in pkg/syncstatus so the CLI can use
// the exact same read-only SQL and formatting without importing this package's
// Rust/CGO connector dependency. Keep these aliases for the command and for
// callers that historically used connector.SyncStatus*.
type ZoneSyncStatus = syncstatus.ZoneSyncStatus
type SyncStatusReport = syncstatus.SyncStatusReport
type SyncStatusOptions = syncstatus.SyncStatusOptions

const syncStatusReadFailureMessage = "Failed to read sync status."

// syncStatusReadErrorReply intentionally discards the underlying database
// error. Drivers may include SQL, filesystem paths, URLs, or other local
// account details, and this reply is sent directly to a Matrix management room.
func syncStatusReadErrorReply(_ error) string {
	return syncStatusReadFailureMessage
}

func GetSyncStatus(ctx context.Context, db *dbutil.Database, opts SyncStatusOptions) (*SyncStatusReport, error) {
	return syncstatus.GetSyncStatus(ctx, db, opts)
}

// cmdSyncStatus is the management-room half of the report. `corten-matrix
// sync-status` prints the same thing without a running bridge.
var cmdSyncStatus = &commands.FullHandler{
	Name:          "sync-status",
	Aliases:       []string{"syncstatus"},
	Func:          fnSyncStatus,
	RequiresLogin: true,
	Help: commands.HelpMeta{
		Section:     commands.HelpSectionChats,
		Description: "Report whether backfill has finished, and what is holding it up if it hasn't: per-zone CloudKit state, how many messages have reached Matrix, and which rows will never be bridged.",
	},
}

func fnSyncStatus(ce *commands.Event) {
	login := ce.User.GetDefaultLogin()
	if login == nil {
		ce.Reply("Not logged in.")
		return
	}
	client, ok := login.Client.(*IMClient)
	if !ok || client == nil {
		ce.Reply("Bridge client not available.")
		return
	}

	opts := SyncStatusOptions{
		BridgeID:            string(ce.Bridge.ID),
		LoginID:             string(login.ID),
		MaxInitialMessages:  ce.Bridge.Config.Backfill.MaxInitialMessages,
		BridgeFilteredChats: client.Main.Config.BridgeFilteredChats,
	}
	// Both of these are only knowable from inside the running process, which is
	// the whole reason the report takes them as pointers.
	client.cloudSyncRunningLock.RLock()
	syncing := client.cloudSyncRunning
	client.cloudSyncRunningLock.RUnlock()
	opts.SyncRunning = &syncing
	batchSending := ce.Bridge.Matrix.GetCapabilities().BatchSending
	opts.BatchSending = &batchSending

	report, err := GetSyncStatus(ce.Ctx, ce.Bridge.DB.Database, opts)
	if err != nil {
		ce.Reply("%s", syncStatusReadErrorReply(err))
		return
	}
	ce.Reply("%s", report.Format())
}
