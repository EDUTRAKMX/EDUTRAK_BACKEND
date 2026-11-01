-- Identity: users, roles, user_roles, auth_sessions

CREATE TABLE roles (
    id   SMALLINT    PRIMARY KEY,
    code VARCHAR(20) NOT NULL UNIQUE
);

INSERT INTO roles (id, code) VALUES
    (1, 'ADMIN'), (2, 'DIRECTOR'), (3, 'TEACHER'), (4, 'TUTOR'), (5, 'STUDENT');

CREATE TABLE users (
    id                   UUID         PRIMARY KEY,
    institutional_number VARCHAR(8)   NOT NULL,
    institutional_email  VARCHAR(255) NOT NULL,
    password_hash        TEXT         NOT NULL,
    first_name           VARCHAR(100) NOT NULL,
    last_name            VARCHAR(150) NOT NULL,
    is_active            BOOLEAN      NOT NULL DEFAULT TRUE,
    must_change_password BOOLEAN      NOT NULL DEFAULT FALSE,
    profile_photo_url    TEXT,
    street               VARCHAR(150),
    exterior_number      VARCHAR(20),
    interior_number      VARCHAR(20),
    neighborhood         VARCHAR(150),
    postal_code          VARCHAR(10),
    curp                 VARCHAR(18),
    created_at           TIMESTAMPTZ  NOT NULL DEFAULT now(),
    updated_at           TIMESTAMPTZ  NOT NULL DEFAULT now(),
    CONSTRAINT users_institutional_number_key UNIQUE (institutional_number),
    CONSTRAINT users_institutional_email_key  UNIQUE (institutional_email),
    CONSTRAINT users_institutional_number_format CHECK (institutional_number ~ '^[0-9]{8}$'),
    CONSTRAINT users_institutional_email_format  CHECK (institutional_email = institutional_number || '@uttcampus.edu.mx')
);

CREATE TABLE user_roles (
    user_id    UUID        NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    role_id    SMALLINT    NOT NULL REFERENCES roles (id),
    granted_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (user_id, role_id)
);
CREATE INDEX user_roles_role_id_idx ON user_roles (role_id);

-- Only the SHA-256 hash (hex) of the refresh token is stored.
CREATE TABLE auth_sessions (
    id                 UUID        PRIMARY KEY,
    user_id            UUID        NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    refresh_token_hash CHAR(64)    NOT NULL,
    user_agent         VARCHAR(255),
    ip_address         INET,
    created_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
    expires_at         TIMESTAMPTZ NOT NULL,
    revoked_at         TIMESTAMPTZ,
    CONSTRAINT auth_sessions_refresh_token_hash_key UNIQUE (refresh_token_hash),
    CONSTRAINT auth_sessions_expiry_check CHECK (expires_at > created_at)
);
CREATE INDEX auth_sessions_user_id_idx ON auth_sessions (user_id) WHERE revoked_at IS NULL;
