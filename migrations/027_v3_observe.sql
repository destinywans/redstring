-- Explicit service metadata for the V3 catalog. Missing values remain NULL;
-- no migration invents SLOs, on-call assignments or health from app names.
CREATE TABLE v3_service_profiles (
 application_id text PRIMARY KEY REFERENCES applications(id) ON DELETE CASCADE,
 tier text CHECK (tier IN ('T0','T1','T2','T3')),
 slo_target_percent numeric(6,3) CHECK (slo_target_percent > 0 AND slo_target_percent <= 100),
 oncall_user_id text REFERENCES users(id),
 created_at timestamptz NOT NULL DEFAULT now(),
 updated_at timestamptz NOT NULL DEFAULT now()
);
