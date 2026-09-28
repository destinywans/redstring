-- One logical application may be registered in multiple environments. Keep
-- SLO targets and ownership labels scoped to the registered environment;
-- historical application-level values remain for legacy readers.
CREATE TABLE v3_service_environment_profiles (
 application_id text NOT NULL,
 environment_id text NOT NULL,
 tier text CHECK (tier IN ('T0','T1','T2','T3')),
 slo_target_percent numeric(6,3) CHECK (slo_target_percent > 0 AND slo_target_percent <= 100),
 owner_label text NOT NULL DEFAULT '',
 p99_target_ms integer CHECK (p99_target_ms > 0),
 oncall_user_id text REFERENCES users(id),
 namespace text NOT NULL DEFAULT '',
 created_at timestamptz NOT NULL DEFAULT now(),
 updated_at timestamptz NOT NULL DEFAULT now(),
 PRIMARY KEY (application_id,environment_id),
 FOREIGN KEY (application_id,environment_id)
   REFERENCES v3_application_environments(application_id,environment_id) ON DELETE CASCADE
);

-- An application-level target cannot be assigned reliably when the same
-- application already spans multiple environments. Backfill only the single-
-- environment case; other targets remain unavailable pending review.
INSERT INTO v3_service_environment_profiles
 (application_id,environment_id,tier,slo_target_percent,owner_label,p99_target_ms,oncall_user_id)
SELECT p.application_id,ae.environment_id,p.tier,p.slo_target_percent,p.owner_label,p.p99_target_ms,p.oncall_user_id
FROM v3_service_profiles p
JOIN v3_application_environments ae ON ae.application_id=p.application_id
JOIN (
 SELECT application_id FROM v3_application_environments
 GROUP BY application_id HAVING count(*)=1
) single_env ON single_env.application_id=p.application_id;
