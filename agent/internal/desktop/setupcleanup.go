package desktop

// OldInstall describes what an earlier Stowline install left on this PC.
type OldInstall struct {
	PilotRootExists bool   // C:\Stowline exists
	ServiceExists   bool   // the StowlineBackup Windows service is registered
	PilotJSON       []byte // C:\Stowline\config\pilot.json, nil if absent
}

// BlocksFreshInstall prevents the fresh installer from deleting enrollment,
// keys, local repositories or history. Unreadable configuration is still
// installation material, not permission to remove it.
func BlocksFreshInstall(old OldInstall) bool {
	return old.PilotRootExists || old.ServiceExists
}
