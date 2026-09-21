-- Personal profile fields are not account or authorization controls.
ALTER TABLE users ADD COLUMN phone text,
 ADD COLUMN avatar_png bytea,
 ADD CONSTRAINT users_avatar_size CHECK (avatar_png IS NULL OR octet_length(avatar_png) <= 4194304);
