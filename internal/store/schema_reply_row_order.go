package store

import "context"

// Reply surfaces are ordered before presentation/publication, not by late ACKs, Telegram
// message IDs or wall clocks. No historical rows are backfilled: those sources
// did not record enough evidence to reconstruct a mixed-path order.
func (s *Store) applyReplyRowOrderSchema(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS telegram_reply_surfaces (
 sequence INTEGER PRIMARY KEY AUTOINCREMENT,
 effect_id TEXT NOT NULL UNIQUE,
 scope_json TEXT NOT NULL,
 namespace_id TEXT NOT NULL,
 namespace_epoch INTEGER NOT NULL,
 registration_revision INTEGER NOT NULL,
 thread_id INTEGER NOT NULL,
 visibility TEXT NOT NULL,
 message_id INTEGER NOT NULL DEFAULT 0 CHECK(message_id>=0),
 turn_id INTEGER NOT NULL CHECK(turn_id>0),
 source TEXT NOT NULL CHECK(source IN ('legacy','outbox')),
 CHECK(thread_id>=0));
 CREATE INDEX IF NOT EXISTS idx_reply_surface_scope ON telegram_reply_surfaces(namespace_id,namespace_epoch,registration_revision,thread_id,visibility,sequence);
 CREATE TRIGGER IF NOT EXISTS reply_surface_immutable BEFORE UPDATE OF sequence,effect_id,scope_json,namespace_id,namespace_epoch,registration_revision,thread_id,visibility,turn_id,source ON telegram_reply_surfaces BEGIN SELECT RAISE(ABORT,'immutable reply surface'); END;
 CREATE TRIGGER IF NOT EXISTS reply_surface_ack_immutable BEFORE UPDATE OF message_id ON telegram_reply_surfaces WHEN OLD.message_id!=0 AND NEW.message_id!=OLD.message_id BEGIN SELECT RAISE(ABORT,'immutable reply receipt'); END;`)
	return err
}
