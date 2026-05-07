package ipc

import "fmt"

// errUnauthorized is wrapped with ErrPermanent so the watcher sends
// the file straight to .failed/ without retrying.
type errUnauthorized struct{ reason string }

func (e *errUnauthorized) Error() string { return "ipc: unauthorized: " + e.reason }
func (e *errUnauthorized) Is(target error) bool {
	return target == ErrPermanent
}

// authzMessage decides whether a message file dropped by sourceFolder
// is allowed to target chatJID. Owner is the only folder allowed to
// target a JID belonging to a different folder.
func authzMessage(sourceFolder, chatJID, ownerFolder string, resolve FolderResolver) error {
	if chatJID == "" {
		return fmt.Errorf("empty chatJid: %w", ErrPermanent)
	}
	// Find the folder that owns chatJID.
	// We can't enumerate; instead, resolve the source folder's own jid
	// and compare. Cross-chat from non-owner is rejected.
	if sourceFolder != ownerFolder {
		ownJID, ok := resolve(sourceFolder)
		if !ok {
			return &errUnauthorized{reason: fmt.Sprintf("source folder %q not registered", sourceFolder)}
		}
		if ownJID != chatJID {
			return &errUnauthorized{reason: "non-owner targeting another chat"}
		}
		return nil
	}
	// Owner: any registered chat is OK. Confirm the JID belongs to a
	// registered chat by reverse-walking known folders via the resolver
	// is impossible (resolver is one-way). Treat owner's chatJID
	// targeting as "trusted as long as the JID is non-empty"; the
	// downstream sink will fail loudly (returning ErrPermanent) if the
	// JID is bogus.
	return nil
}

// authzTaskSchedule decides whether sourceFolder may schedule a task
// against targetFolder (empty = source). Cross-folder requires owner.
func authzTaskSchedule(sourceFolder, targetFolder, ownerFolder string, resolve FolderResolver) error {
	if targetFolder == "" {
		targetFolder = sourceFolder
	}
	if _, ok := resolve(targetFolder); !ok {
		return &errUnauthorized{reason: fmt.Sprintf("target folder %q not registered", targetFolder)}
	}
	if sourceFolder == ownerFolder {
		return nil
	}
	if sourceFolder != targetFolder {
		return &errUnauthorized{reason: "non-owner cross-folder schedule"}
	}
	return nil
}
