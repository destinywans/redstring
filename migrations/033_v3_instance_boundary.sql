-- A production and a non-production process must never reuse the same
-- database, even across a restart. The instance config is intentionally
-- bound once; changing tiers/domains requires an explicit reviewed migration.
CREATE TABLE IF NOT EXISTS v3_instance_boundary (
 singleton boolean PRIMARY KEY DEFAULT true CHECK (singleton),
 tier text NOT NULL CHECK (tier IN ('production','nonprod')),
 domain text NOT NULL CHECK (domain <> ''),
 bound_at timestamptz NOT NULL DEFAULT now()
);
