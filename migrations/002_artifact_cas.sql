-- Append metadata required by the P3 trusted CAS publication protocol.
ALTER TABLE artifacts ADD COLUMN content_type text NOT NULL DEFAULT 'application/octet-stream'
 CHECK(content_type IN ('application/json','text/plain','application/octet-stream'));
ALTER TABLE artifacts ADD COLUMN tool_schema_version text NOT NULL DEFAULT 'legacy_fixture.v1'
 CHECK(length(tool_schema_version) BETWEEN 1 AND 128);
