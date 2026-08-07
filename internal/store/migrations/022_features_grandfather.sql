-- Optional features ship OFF (config.Defaults().Features), so a fresh install
-- exposes none until the owner enables one. That default would otherwise yank
-- research labs and trials out from under an EXISTING installation that has been
-- recording trials -- an upgrade must never hide data the owner is already using.
--
-- One-time grandfather clause: seed the features_config override row to
-- research-on iff this database already holds trial data AND no override row
-- exists yet. A fresh database runs the same statement over an empty trials
-- table and seeds nothing, so installs that never used the feature converge to
-- off. The seeded row is an ordinary stored override afterwards: the console can
-- reset it, which means back to file/default (off).
--
-- The JSON value must match config.Features' json tags (store.SetFeaturesConfig
-- writes the same shape).
INSERT INTO settings (key, value)
SELECT 'features_config', '{"research":true}'
WHERE EXISTS (SELECT 1 FROM trials)
  AND NOT EXISTS (SELECT 1 FROM settings WHERE key = 'features_config');
