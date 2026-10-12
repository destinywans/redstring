-- PA-100: instances register applications separately, so their IDs differ;
-- a kind export now carries the application key that production resolves.
-- Export format 2 is that manifest. An earlier export of the same source
-- digest stays as history, and exporting the object again is a new record,
-- branch and MR rather than a replay of the old one.
ALTER TABLE v3_config_exports ADD COLUMN export_format smallint NOT NULL DEFAULT 1
 CHECK (export_format IN (1,2) AND (kind<>'template' OR export_format=1));
ALTER TABLE v3_config_exports DROP CONSTRAINT v3_config_exports_kind_object_id_environment_id_source_dige_key;
ALTER TABLE v3_config_exports ADD CONSTRAINT v3_config_exports_source_format_key
 UNIQUE (kind,object_id,environment_id,source_digest,export_format);
