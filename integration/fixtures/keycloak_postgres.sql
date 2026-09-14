-- Keycloak 26.1.4 schema (PostgreSQL), schema-only: captured with pg_dump from the
-- database Keycloak's own migrations create. Keycloak is Apache-2.0 licensed.
-- Used as a real-world stress schema: 87 tables, varchar(36) ids, composite keys,
-- dense FKs. Regenerate by booting Keycloak against an empty database.

CREATE TABLE admin_event_entity (
    id character varying(36) NOT NULL,
    admin_event_time bigint,
    realm_id character varying(255),
    operation_type character varying(255),
    auth_realm_id character varying(255),
    auth_client_id character varying(255),
    auth_user_id character varying(255),
    ip_address character varying(255),
    resource_path character varying(2550),
    representation text,
    error character varying(255),
    resource_type character varying(64),
    details_json text
);

CREATE TABLE associated_policy (
    policy_id character varying(36) NOT NULL,
    associated_policy_id character varying(36) NOT NULL
);

CREATE TABLE authentication_execution (
    id character varying(36) NOT NULL,
    alias character varying(255),
    authenticator character varying(36),
    realm_id character varying(36),
    flow_id character varying(36),
    requirement integer,
    priority integer,
    authenticator_flow boolean DEFAULT false NOT NULL,
    auth_flow_id character varying(36),
    auth_config character varying(36)
);

CREATE TABLE authentication_flow (
    id character varying(36) NOT NULL,
    alias character varying(255),
    description character varying(255),
    realm_id character varying(36),
    provider_id character varying(36) DEFAULT 'basic-flow'::character varying NOT NULL,
    top_level boolean DEFAULT false NOT NULL,
    built_in boolean DEFAULT false NOT NULL
);

CREATE TABLE authenticator_config (
    id character varying(36) NOT NULL,
    alias character varying(255),
    realm_id character varying(36)
);

CREATE TABLE authenticator_config_entry (
    authenticator_id character varying(36) NOT NULL,
    value text,
    name character varying(255) NOT NULL
);

CREATE TABLE broker_link (
    identity_provider character varying(255) NOT NULL,
    storage_provider_id character varying(255),
    realm_id character varying(36) NOT NULL,
    broker_user_id character varying(255),
    broker_username character varying(255),
    token text,
    user_id character varying(255) NOT NULL
);

CREATE TABLE client (
    id character varying(36) NOT NULL,
    enabled boolean DEFAULT false NOT NULL,
    full_scope_allowed boolean DEFAULT false NOT NULL,
    client_id character varying(255),
    not_before integer,
    public_client boolean DEFAULT false NOT NULL,
    secret character varying(255),
    base_url character varying(255),
    bearer_only boolean DEFAULT false NOT NULL,
    management_url character varying(255),
    surrogate_auth_required boolean DEFAULT false NOT NULL,
    realm_id character varying(36),
    protocol character varying(255),
    node_rereg_timeout integer DEFAULT 0,
    frontchannel_logout boolean DEFAULT false NOT NULL,
    consent_required boolean DEFAULT false NOT NULL,
    name character varying(255),
    service_accounts_enabled boolean DEFAULT false NOT NULL,
    client_authenticator_type character varying(255),
    root_url character varying(255),
    description character varying(255),
    registration_token character varying(255),
    standard_flow_enabled boolean DEFAULT true NOT NULL,
    implicit_flow_enabled boolean DEFAULT false NOT NULL,
    direct_access_grants_enabled boolean DEFAULT false NOT NULL,
    always_display_in_console boolean DEFAULT false NOT NULL
);

CREATE TABLE client_attributes (
    client_id character varying(36) NOT NULL,
    name character varying(255) NOT NULL,
    value text
);

CREATE TABLE client_auth_flow_bindings (
    client_id character varying(36) NOT NULL,
    flow_id character varying(36),
    binding_name character varying(255) NOT NULL
);

CREATE TABLE client_initial_access (
    id character varying(36) NOT NULL,
    realm_id character varying(36) NOT NULL,
    "timestamp" integer,
    expiration integer,
    count integer,
    remaining_count integer
);

CREATE TABLE client_node_registrations (
    client_id character varying(36) NOT NULL,
    value integer,
    name character varying(255) NOT NULL
);

CREATE TABLE client_scope (
    id character varying(36) NOT NULL,
    name character varying(255),
    realm_id character varying(36),
    description character varying(255),
    protocol character varying(255)
);

CREATE TABLE client_scope_attributes (
    scope_id character varying(36) NOT NULL,
    value character varying(2048),
    name character varying(255) NOT NULL
);

CREATE TABLE client_scope_client (
    client_id character varying(255) NOT NULL,
    scope_id character varying(255) NOT NULL,
    default_scope boolean DEFAULT false NOT NULL
);

CREATE TABLE client_scope_role_mapping (
    scope_id character varying(36) NOT NULL,
    role_id character varying(36) NOT NULL
);

CREATE TABLE component (
    id character varying(36) NOT NULL,
    name character varying(255),
    parent_id character varying(36),
    provider_id character varying(36),
    provider_type character varying(255),
    realm_id character varying(36),
    sub_type character varying(255)
);

CREATE TABLE component_config (
    id character varying(36) NOT NULL,
    component_id character varying(36) NOT NULL,
    name character varying(255) NOT NULL,
    value text
);

CREATE TABLE composite_role (
    composite character varying(36) NOT NULL,
    child_role character varying(36) NOT NULL
);

CREATE TABLE credential (
    id character varying(36) NOT NULL,
    salt bytea,
    type character varying(255),
    user_id character varying(36),
    created_date bigint,
    user_label character varying(255),
    secret_data text,
    credential_data text,
    priority integer
);

CREATE TABLE databasechangelog (
    id character varying(255) NOT NULL,
    author character varying(255) NOT NULL,
    filename character varying(255) NOT NULL,
    dateexecuted timestamp without time zone NOT NULL,
    orderexecuted integer NOT NULL,
    exectype character varying(10) NOT NULL,
    md5sum character varying(35),
    description character varying(255),
    comments character varying(255),
    tag character varying(255),
    liquibase character varying(20),
    contexts character varying(255),
    labels character varying(255),
    deployment_id character varying(10)
);

CREATE TABLE databasechangeloglock (
    id integer NOT NULL,
    locked boolean NOT NULL,
    lockgranted timestamp without time zone,
    lockedby character varying(255)
);

CREATE TABLE default_client_scope (
    realm_id character varying(36) NOT NULL,
    scope_id character varying(36) NOT NULL,
    default_scope boolean DEFAULT false NOT NULL
);

CREATE TABLE event_entity (
    id character varying(36) NOT NULL,
    client_id character varying(255),
    details_json character varying(2550),
    error character varying(255),
    ip_address character varying(255),
    realm_id character varying(255),
    session_id character varying(255),
    event_time bigint,
    type character varying(255),
    user_id character varying(255),
    details_json_long_value text
);

CREATE TABLE fed_user_attribute (
    id character varying(36) NOT NULL,
    name character varying(255) NOT NULL,
    user_id character varying(255) NOT NULL,
    realm_id character varying(36) NOT NULL,
    storage_provider_id character varying(36),
    value character varying(2024),
    long_value_hash bytea,
    long_value_hash_lower_case bytea,
    long_value text
);

CREATE TABLE fed_user_consent (
    id character varying(36) NOT NULL,
    client_id character varying(255),
    user_id character varying(255) NOT NULL,
    realm_id character varying(36) NOT NULL,
    storage_provider_id character varying(36),
    created_date bigint,
    last_updated_date bigint,
    client_storage_provider character varying(36),
    external_client_id character varying(255)
);

CREATE TABLE fed_user_consent_cl_scope (
    user_consent_id character varying(36) NOT NULL,
    scope_id character varying(36) NOT NULL
);

CREATE TABLE fed_user_credential (
    id character varying(36) NOT NULL,
    salt bytea,
    type character varying(255),
    created_date bigint,
    user_id character varying(255) NOT NULL,
    realm_id character varying(36) NOT NULL,
    storage_provider_id character varying(36),
    user_label character varying(255),
    secret_data text,
    credential_data text,
    priority integer
);

CREATE TABLE fed_user_group_membership (
    group_id character varying(36) NOT NULL,
    user_id character varying(255) NOT NULL,
    realm_id character varying(36) NOT NULL,
    storage_provider_id character varying(36)
);

CREATE TABLE fed_user_required_action (
    required_action character varying(255) DEFAULT ' '::character varying NOT NULL,
    user_id character varying(255) NOT NULL,
    realm_id character varying(36) NOT NULL,
    storage_provider_id character varying(36)
);

CREATE TABLE fed_user_role_mapping (
    role_id character varying(36) NOT NULL,
    user_id character varying(255) NOT NULL,
    realm_id character varying(36) NOT NULL,
    storage_provider_id character varying(36)
);

CREATE TABLE federated_identity (
    identity_provider character varying(255) NOT NULL,
    realm_id character varying(36),
    federated_user_id character varying(255),
    federated_username character varying(255),
    token text,
    user_id character varying(36) NOT NULL
);

CREATE TABLE federated_user (
    id character varying(255) NOT NULL,
    storage_provider_id character varying(255),
    realm_id character varying(36) NOT NULL
);

CREATE TABLE group_attribute (
    id character varying(36) DEFAULT 'sybase-needs-something-here'::character varying NOT NULL,
    name character varying(255) NOT NULL,
    value character varying(255),
    group_id character varying(36) NOT NULL
);

CREATE TABLE group_role_mapping (
    role_id character varying(36) NOT NULL,
    group_id character varying(36) NOT NULL
);

CREATE TABLE identity_provider (
    internal_id character varying(36) NOT NULL,
    enabled boolean DEFAULT false NOT NULL,
    provider_alias character varying(255),
    provider_id character varying(255),
    store_token boolean DEFAULT false NOT NULL,
    authenticate_by_default boolean DEFAULT false NOT NULL,
    realm_id character varying(36),
    add_token_role boolean DEFAULT true NOT NULL,
    trust_email boolean DEFAULT false NOT NULL,
    first_broker_login_flow_id character varying(36),
    post_broker_login_flow_id character varying(36),
    provider_display_name character varying(255),
    link_only boolean DEFAULT false NOT NULL,
    organization_id character varying(255),
    hide_on_login boolean DEFAULT false
);

CREATE TABLE identity_provider_config (
    identity_provider_id character varying(36) NOT NULL,
    value text,
    name character varying(255) NOT NULL
);

CREATE TABLE identity_provider_mapper (
    id character varying(36) NOT NULL,
    name character varying(255) NOT NULL,
    idp_alias character varying(255) NOT NULL,
    idp_mapper_name character varying(255) NOT NULL,
    realm_id character varying(36) NOT NULL
);

CREATE TABLE idp_mapper_config (
    idp_mapper_id character varying(36) NOT NULL,
    value text,
    name character varying(255) NOT NULL
);

CREATE TABLE jgroups_ping (
    address character varying(200) NOT NULL,
    name character varying(200),
    cluster_name character varying(200) NOT NULL,
    ip character varying(200) NOT NULL,
    coord boolean
);

CREATE TABLE keycloak_group (
    id character varying(36) NOT NULL,
    name character varying(255),
    parent_group character varying(36) NOT NULL,
    realm_id character varying(36),
    type integer DEFAULT 0 NOT NULL
);

CREATE TABLE keycloak_role (
    id character varying(36) NOT NULL,
    client_realm_constraint character varying(255),
    client_role boolean DEFAULT false NOT NULL,
    description character varying(255),
    name character varying(255),
    realm_id character varying(255),
    client character varying(36),
    realm character varying(36)
);

CREATE TABLE migration_model (
    id character varying(36) NOT NULL,
    version character varying(36),
    update_time bigint DEFAULT 0 NOT NULL
);

CREATE TABLE offline_client_session (
    user_session_id character varying(36) NOT NULL,
    client_id character varying(255) NOT NULL,
    offline_flag character varying(4) NOT NULL,
    "timestamp" integer,
    data text,
    client_storage_provider character varying(36) DEFAULT 'local'::character varying NOT NULL,
    external_client_id character varying(255) DEFAULT 'local'::character varying NOT NULL,
    version integer DEFAULT 0
);

CREATE TABLE offline_user_session (
    user_session_id character varying(36) NOT NULL,
    user_id character varying(255) NOT NULL,
    realm_id character varying(36) NOT NULL,
    created_on integer NOT NULL,
    offline_flag character varying(4) NOT NULL,
    data text,
    last_session_refresh integer DEFAULT 0 NOT NULL,
    broker_session_id character varying(1024),
    version integer DEFAULT 0
);

CREATE TABLE org (
    id character varying(255) NOT NULL,
    enabled boolean NOT NULL,
    realm_id character varying(255) NOT NULL,
    group_id character varying(255) NOT NULL,
    name character varying(255) NOT NULL,
    description character varying(4000),
    alias character varying(255) NOT NULL,
    redirect_url character varying(2048)
);

CREATE TABLE org_domain (
    id character varying(36) NOT NULL,
    name character varying(255) NOT NULL,
    verified boolean NOT NULL,
    org_id character varying(255) NOT NULL
);

CREATE TABLE policy_config (
    policy_id character varying(36) NOT NULL,
    name character varying(255) NOT NULL,
    value text
);

CREATE TABLE protocol_mapper (
    id character varying(36) NOT NULL,
    name character varying(255) NOT NULL,
    protocol character varying(255) NOT NULL,
    protocol_mapper_name character varying(255) NOT NULL,
    client_id character varying(36),
    client_scope_id character varying(36)
);

CREATE TABLE protocol_mapper_config (
    protocol_mapper_id character varying(36) NOT NULL,
    value text,
    name character varying(255) NOT NULL
);

CREATE TABLE realm (
    id character varying(36) NOT NULL,
    access_code_lifespan integer,
    user_action_lifespan integer,
    access_token_lifespan integer,
    account_theme character varying(255),
    admin_theme character varying(255),
    email_theme character varying(255),
    enabled boolean DEFAULT false NOT NULL,
    events_enabled boolean DEFAULT false NOT NULL,
    events_expiration bigint,
    login_theme character varying(255),
    name character varying(255),
    not_before integer,
    password_policy character varying(2550),
    registration_allowed boolean DEFAULT false NOT NULL,
    remember_me boolean DEFAULT false NOT NULL,
    reset_password_allowed boolean DEFAULT false NOT NULL,
    social boolean DEFAULT false NOT NULL,
    ssl_required character varying(255),
    sso_idle_timeout integer,
    sso_max_lifespan integer,
    update_profile_on_soc_login boolean DEFAULT false NOT NULL,
    verify_email boolean DEFAULT false NOT NULL,
    master_admin_client character varying(36),
    login_lifespan integer,
    internationalization_enabled boolean DEFAULT false NOT NULL,
    default_locale character varying(255),
    reg_email_as_username boolean DEFAULT false NOT NULL,
    admin_events_enabled boolean DEFAULT false NOT NULL,
    admin_events_details_enabled boolean DEFAULT false NOT NULL,
    edit_username_allowed boolean DEFAULT false NOT NULL,
    otp_policy_counter integer DEFAULT 0,
    otp_policy_window integer DEFAULT 1,
    otp_policy_period integer DEFAULT 30,
    otp_policy_digits integer DEFAULT 6,
    otp_policy_alg character varying(36) DEFAULT 'HmacSHA1'::character varying,
    otp_policy_type character varying(36) DEFAULT 'totp'::character varying,
    browser_flow character varying(36),
    registration_flow character varying(36),
    direct_grant_flow character varying(36),
    reset_credentials_flow character varying(36),
    client_auth_flow character varying(36),
    offline_session_idle_timeout integer DEFAULT 0,
    revoke_refresh_token boolean DEFAULT false NOT NULL,
    access_token_life_implicit integer DEFAULT 0,
    login_with_email_allowed boolean DEFAULT true NOT NULL,
    duplicate_emails_allowed boolean DEFAULT false NOT NULL,
    docker_auth_flow character varying(36),
    refresh_token_max_reuse integer DEFAULT 0,
    allow_user_managed_access boolean DEFAULT false NOT NULL,
    sso_max_lifespan_remember_me integer DEFAULT 0 NOT NULL,
    sso_idle_timeout_remember_me integer DEFAULT 0 NOT NULL,
    default_role character varying(255)
);

CREATE TABLE realm_attribute (
    name character varying(255) NOT NULL,
    realm_id character varying(36) NOT NULL,
    value text
);

CREATE TABLE realm_default_groups (
    realm_id character varying(36) NOT NULL,
    group_id character varying(36) NOT NULL
);

CREATE TABLE realm_enabled_event_types (
    realm_id character varying(36) NOT NULL,
    value character varying(255) NOT NULL
);

CREATE TABLE realm_events_listeners (
    realm_id character varying(36) NOT NULL,
    value character varying(255) NOT NULL
);

CREATE TABLE realm_localizations (
    realm_id character varying(255) NOT NULL,
    locale character varying(255) NOT NULL,
    texts text NOT NULL
);

CREATE TABLE realm_required_credential (
    type character varying(255) NOT NULL,
    form_label character varying(255),
    input boolean DEFAULT false NOT NULL,
    secret boolean DEFAULT false NOT NULL,
    realm_id character varying(36) NOT NULL
);

CREATE TABLE realm_smtp_config (
    realm_id character varying(36) NOT NULL,
    value character varying(255),
    name character varying(255) NOT NULL
);

CREATE TABLE realm_supported_locales (
    realm_id character varying(36) NOT NULL,
    value character varying(255) NOT NULL
);

CREATE TABLE redirect_uris (
    client_id character varying(36) NOT NULL,
    value character varying(255) NOT NULL
);

CREATE TABLE required_action_config (
    required_action_id character varying(36) NOT NULL,
    value text,
    name character varying(255) NOT NULL
);

CREATE TABLE required_action_provider (
    id character varying(36) NOT NULL,
    alias character varying(255),
    name character varying(255),
    realm_id character varying(36),
    enabled boolean DEFAULT false NOT NULL,
    default_action boolean DEFAULT false NOT NULL,
    provider_id character varying(255),
    priority integer
);

CREATE TABLE resource_attribute (
    id character varying(36) DEFAULT 'sybase-needs-something-here'::character varying NOT NULL,
    name character varying(255) NOT NULL,
    value character varying(255),
    resource_id character varying(36) NOT NULL
);

CREATE TABLE resource_policy (
    resource_id character varying(36) NOT NULL,
    policy_id character varying(36) NOT NULL
);

CREATE TABLE resource_scope (
    resource_id character varying(36) NOT NULL,
    scope_id character varying(36) NOT NULL
);

CREATE TABLE resource_server (
    id character varying(36) NOT NULL,
    allow_rs_remote_mgmt boolean DEFAULT false NOT NULL,
    policy_enforce_mode smallint NOT NULL,
    decision_strategy smallint DEFAULT 1 NOT NULL
);

CREATE TABLE resource_server_perm_ticket (
    id character varying(36) NOT NULL,
    owner character varying(255) NOT NULL,
    requester character varying(255) NOT NULL,
    created_timestamp bigint NOT NULL,
    granted_timestamp bigint,
    resource_id character varying(36) NOT NULL,
    scope_id character varying(36),
    resource_server_id character varying(36) NOT NULL,
    policy_id character varying(36)
);

CREATE TABLE resource_server_policy (
    id character varying(36) NOT NULL,
    name character varying(255) NOT NULL,
    description character varying(255),
    type character varying(255) NOT NULL,
    decision_strategy smallint,
    logic smallint,
    resource_server_id character varying(36) NOT NULL,
    owner character varying(255)
);

CREATE TABLE resource_server_resource (
    id character varying(36) NOT NULL,
    name character varying(255) NOT NULL,
    type character varying(255),
    icon_uri character varying(255),
    owner character varying(255) NOT NULL,
    resource_server_id character varying(36) NOT NULL,
    owner_managed_access boolean DEFAULT false NOT NULL,
    display_name character varying(255)
);

CREATE TABLE resource_server_scope (
    id character varying(36) NOT NULL,
    name character varying(255) NOT NULL,
    icon_uri character varying(255),
    resource_server_id character varying(36) NOT NULL,
    display_name character varying(255)
);

CREATE TABLE resource_uris (
    resource_id character varying(36) NOT NULL,
    value character varying(255) NOT NULL
);

CREATE TABLE revoked_token (
    id character varying(255) NOT NULL,
    expire bigint NOT NULL
);

CREATE TABLE role_attribute (
    id character varying(36) NOT NULL,
    role_id character varying(36) NOT NULL,
    name character varying(255) NOT NULL,
    value character varying(255)
);

CREATE TABLE scope_mapping (
    client_id character varying(36) NOT NULL,
    role_id character varying(36) NOT NULL
);

CREATE TABLE scope_policy (
    scope_id character varying(36) NOT NULL,
    policy_id character varying(36) NOT NULL
);

CREATE TABLE user_attribute (
    name character varying(255) NOT NULL,
    value character varying(255),
    user_id character varying(36) NOT NULL,
    id character varying(36) DEFAULT 'sybase-needs-something-here'::character varying NOT NULL,
    long_value_hash bytea,
    long_value_hash_lower_case bytea,
    long_value text
);

CREATE TABLE user_consent (
    id character varying(36) NOT NULL,
    client_id character varying(255),
    user_id character varying(36) NOT NULL,
    created_date bigint,
    last_updated_date bigint,
    client_storage_provider character varying(36),
    external_client_id character varying(255)
);

CREATE TABLE user_consent_client_scope (
    user_consent_id character varying(36) NOT NULL,
    scope_id character varying(36) NOT NULL
);

CREATE TABLE user_entity (
    id character varying(36) NOT NULL,
    email character varying(255),
    email_constraint character varying(255),
    email_verified boolean DEFAULT false NOT NULL,
    enabled boolean DEFAULT false NOT NULL,
    federation_link character varying(255),
    first_name character varying(255),
    last_name character varying(255),
    realm_id character varying(255),
    username character varying(255),
    created_timestamp bigint,
    service_account_client_link character varying(255),
    not_before integer DEFAULT 0 NOT NULL
);

CREATE TABLE user_federation_config (
    user_federation_provider_id character varying(36) NOT NULL,
    value character varying(255),
    name character varying(255) NOT NULL
);

CREATE TABLE user_federation_mapper (
    id character varying(36) NOT NULL,
    name character varying(255) NOT NULL,
    federation_provider_id character varying(36) NOT NULL,
    federation_mapper_type character varying(255) NOT NULL,
    realm_id character varying(36) NOT NULL
);

CREATE TABLE user_federation_mapper_config (
    user_federation_mapper_id character varying(36) NOT NULL,
    value character varying(255),
    name character varying(255) NOT NULL
);

CREATE TABLE user_federation_provider (
    id character varying(36) NOT NULL,
    changed_sync_period integer,
    display_name character varying(255),
    full_sync_period integer,
    last_sync integer,
    priority integer,
    provider_name character varying(255),
    realm_id character varying(36)
);

CREATE TABLE user_group_membership (
    group_id character varying(36) NOT NULL,
    user_id character varying(36) NOT NULL,
    membership_type character varying(255) NOT NULL
);

CREATE TABLE user_required_action (
    user_id character varying(36) NOT NULL,
    required_action character varying(255) DEFAULT ' '::character varying NOT NULL
);

CREATE TABLE user_role_mapping (
    role_id character varying(255) NOT NULL,
    user_id character varying(36) NOT NULL
);

CREATE TABLE web_origins (
    client_id character varying(36) NOT NULL,
    value character varying(255) NOT NULL
);

ALTER TABLE ONLY org_domain
    ADD CONSTRAINT "ORG_DOMAIN_pkey" PRIMARY KEY (id, name);

ALTER TABLE ONLY org
    ADD CONSTRAINT "ORG_pkey" PRIMARY KEY (id);

ALTER TABLE ONLY keycloak_role
    ADD CONSTRAINT "UK_J3RWUVD56ONTGSUHOGM184WW2-2" UNIQUE (name, client_realm_constraint);

ALTER TABLE ONLY client_auth_flow_bindings
    ADD CONSTRAINT c_cli_flow_bind PRIMARY KEY (client_id, binding_name);

ALTER TABLE ONLY client_scope_client
    ADD CONSTRAINT c_cli_scope_bind PRIMARY KEY (client_id, scope_id);

ALTER TABLE ONLY client_initial_access
    ADD CONSTRAINT cnstr_client_init_acc_pk PRIMARY KEY (id);

ALTER TABLE ONLY realm_default_groups
    ADD CONSTRAINT con_group_id_def_groups UNIQUE (group_id);

ALTER TABLE ONLY broker_link
    ADD CONSTRAINT constr_broker_link_pk PRIMARY KEY (identity_provider, user_id);

ALTER TABLE ONLY component_config
    ADD CONSTRAINT constr_component_config_pk PRIMARY KEY (id);

ALTER TABLE ONLY component
    ADD CONSTRAINT constr_component_pk PRIMARY KEY (id);

ALTER TABLE ONLY fed_user_required_action
    ADD CONSTRAINT constr_fed_required_action PRIMARY KEY (required_action, user_id);

ALTER TABLE ONLY fed_user_attribute
    ADD CONSTRAINT constr_fed_user_attr_pk PRIMARY KEY (id);

ALTER TABLE ONLY fed_user_consent
    ADD CONSTRAINT constr_fed_user_consent_pk PRIMARY KEY (id);

ALTER TABLE ONLY fed_user_credential
    ADD CONSTRAINT constr_fed_user_cred_pk PRIMARY KEY (id);

ALTER TABLE ONLY fed_user_group_membership
    ADD CONSTRAINT constr_fed_user_group PRIMARY KEY (group_id, user_id);

ALTER TABLE ONLY fed_user_role_mapping
    ADD CONSTRAINT constr_fed_user_role PRIMARY KEY (role_id, user_id);

ALTER TABLE ONLY federated_user
    ADD CONSTRAINT constr_federated_user PRIMARY KEY (id);

ALTER TABLE ONLY realm_default_groups
    ADD CONSTRAINT constr_realm_default_groups PRIMARY KEY (realm_id, group_id);

ALTER TABLE ONLY realm_enabled_event_types
    ADD CONSTRAINT constr_realm_enabl_event_types PRIMARY KEY (realm_id, value);

ALTER TABLE ONLY realm_events_listeners
    ADD CONSTRAINT constr_realm_events_listeners PRIMARY KEY (realm_id, value);

ALTER TABLE ONLY realm_supported_locales
    ADD CONSTRAINT constr_realm_supported_locales PRIMARY KEY (realm_id, value);

ALTER TABLE ONLY identity_provider
    ADD CONSTRAINT constraint_2b PRIMARY KEY (internal_id);

ALTER TABLE ONLY client_attributes
    ADD CONSTRAINT constraint_3c PRIMARY KEY (client_id, name);

ALTER TABLE ONLY event_entity
    ADD CONSTRAINT constraint_4 PRIMARY KEY (id);

ALTER TABLE ONLY federated_identity
    ADD CONSTRAINT constraint_40 PRIMARY KEY (identity_provider, user_id);

ALTER TABLE ONLY realm
    ADD CONSTRAINT constraint_4a PRIMARY KEY (id);

ALTER TABLE ONLY user_federation_provider
    ADD CONSTRAINT constraint_5c PRIMARY KEY (id);

ALTER TABLE ONLY client
    ADD CONSTRAINT constraint_7 PRIMARY KEY (id);

ALTER TABLE ONLY scope_mapping
    ADD CONSTRAINT constraint_81 PRIMARY KEY (client_id, role_id);

ALTER TABLE ONLY client_node_registrations
    ADD CONSTRAINT constraint_84 PRIMARY KEY (client_id, name);

ALTER TABLE ONLY realm_attribute
    ADD CONSTRAINT constraint_9 PRIMARY KEY (name, realm_id);

ALTER TABLE ONLY realm_required_credential
    ADD CONSTRAINT constraint_92 PRIMARY KEY (realm_id, type);

ALTER TABLE ONLY keycloak_role
    ADD CONSTRAINT constraint_a PRIMARY KEY (id);

ALTER TABLE ONLY admin_event_entity
    ADD CONSTRAINT constraint_admin_event_entity PRIMARY KEY (id);

ALTER TABLE ONLY authenticator_config_entry
    ADD CONSTRAINT constraint_auth_cfg_pk PRIMARY KEY (authenticator_id, name);

ALTER TABLE ONLY authentication_execution
    ADD CONSTRAINT constraint_auth_exec_pk PRIMARY KEY (id);

ALTER TABLE ONLY authentication_flow
    ADD CONSTRAINT constraint_auth_flow_pk PRIMARY KEY (id);

ALTER TABLE ONLY authenticator_config
    ADD CONSTRAINT constraint_auth_pk PRIMARY KEY (id);

ALTER TABLE ONLY user_role_mapping
    ADD CONSTRAINT constraint_c PRIMARY KEY (role_id, user_id);

ALTER TABLE ONLY composite_role
    ADD CONSTRAINT constraint_composite_role PRIMARY KEY (composite, child_role);

ALTER TABLE ONLY identity_provider_config
    ADD CONSTRAINT constraint_d PRIMARY KEY (identity_provider_id, name);

ALTER TABLE ONLY policy_config
    ADD CONSTRAINT constraint_dpc PRIMARY KEY (policy_id, name);

ALTER TABLE ONLY realm_smtp_config
    ADD CONSTRAINT constraint_e PRIMARY KEY (realm_id, name);

ALTER TABLE ONLY credential
    ADD CONSTRAINT constraint_f PRIMARY KEY (id);

ALTER TABLE ONLY user_federation_config
    ADD CONSTRAINT constraint_f9 PRIMARY KEY (user_federation_provider_id, name);

ALTER TABLE ONLY resource_server_perm_ticket
    ADD CONSTRAINT constraint_fapmt PRIMARY KEY (id);

ALTER TABLE ONLY resource_server_resource
    ADD CONSTRAINT constraint_farsr PRIMARY KEY (id);

ALTER TABLE ONLY resource_server_policy
    ADD CONSTRAINT constraint_farsrp PRIMARY KEY (id);

ALTER TABLE ONLY associated_policy
    ADD CONSTRAINT constraint_farsrpap PRIMARY KEY (policy_id, associated_policy_id);

ALTER TABLE ONLY resource_policy
    ADD CONSTRAINT constraint_farsrpp PRIMARY KEY (resource_id, policy_id);

ALTER TABLE ONLY resource_server_scope
    ADD CONSTRAINT constraint_farsrs PRIMARY KEY (id);

ALTER TABLE ONLY resource_scope
    ADD CONSTRAINT constraint_farsrsp PRIMARY KEY (resource_id, scope_id);

ALTER TABLE ONLY scope_policy
    ADD CONSTRAINT constraint_farsrsps PRIMARY KEY (scope_id, policy_id);

ALTER TABLE ONLY user_entity
    ADD CONSTRAINT constraint_fb PRIMARY KEY (id);

ALTER TABLE ONLY user_federation_mapper_config
    ADD CONSTRAINT constraint_fedmapper_cfg_pm PRIMARY KEY (user_federation_mapper_id, name);

ALTER TABLE ONLY user_federation_mapper
    ADD CONSTRAINT constraint_fedmapperpm PRIMARY KEY (id);

ALTER TABLE ONLY fed_user_consent_cl_scope
    ADD CONSTRAINT constraint_fgrntcsnt_clsc_pm PRIMARY KEY (user_consent_id, scope_id);

ALTER TABLE ONLY user_consent_client_scope
    ADD CONSTRAINT constraint_grntcsnt_clsc_pm PRIMARY KEY (user_consent_id, scope_id);

ALTER TABLE ONLY user_consent
    ADD CONSTRAINT constraint_grntcsnt_pm PRIMARY KEY (id);

ALTER TABLE ONLY keycloak_group
    ADD CONSTRAINT constraint_group PRIMARY KEY (id);

ALTER TABLE ONLY group_attribute
    ADD CONSTRAINT constraint_group_attribute_pk PRIMARY KEY (id);

ALTER TABLE ONLY group_role_mapping
    ADD CONSTRAINT constraint_group_role PRIMARY KEY (role_id, group_id);

ALTER TABLE ONLY identity_provider_mapper
    ADD CONSTRAINT constraint_idpm PRIMARY KEY (id);

ALTER TABLE ONLY idp_mapper_config
    ADD CONSTRAINT constraint_idpmconfig PRIMARY KEY (idp_mapper_id, name);

ALTER TABLE ONLY jgroups_ping
    ADD CONSTRAINT constraint_jgroups_ping PRIMARY KEY (address);

ALTER TABLE ONLY migration_model
    ADD CONSTRAINT constraint_migmod PRIMARY KEY (id);

ALTER TABLE ONLY offline_client_session
    ADD CONSTRAINT constraint_offl_cl_ses_pk3 PRIMARY KEY (user_session_id, client_id, client_storage_provider, external_client_id, offline_flag);

ALTER TABLE ONLY offline_user_session
    ADD CONSTRAINT constraint_offl_us_ses_pk2 PRIMARY KEY (user_session_id, offline_flag);

ALTER TABLE ONLY protocol_mapper
    ADD CONSTRAINT constraint_pcm PRIMARY KEY (id);

ALTER TABLE ONLY protocol_mapper_config
    ADD CONSTRAINT constraint_pmconfig PRIMARY KEY (protocol_mapper_id, name);

ALTER TABLE ONLY redirect_uris
    ADD CONSTRAINT constraint_redirect_uris PRIMARY KEY (client_id, value);

ALTER TABLE ONLY required_action_config
    ADD CONSTRAINT constraint_req_act_cfg_pk PRIMARY KEY (required_action_id, name);

ALTER TABLE ONLY required_action_provider
    ADD CONSTRAINT constraint_req_act_prv_pk PRIMARY KEY (id);

ALTER TABLE ONLY user_required_action
    ADD CONSTRAINT constraint_required_action PRIMARY KEY (required_action, user_id);

ALTER TABLE ONLY resource_uris
    ADD CONSTRAINT constraint_resour_uris_pk PRIMARY KEY (resource_id, value);

ALTER TABLE ONLY role_attribute
    ADD CONSTRAINT constraint_role_attribute_pk PRIMARY KEY (id);

ALTER TABLE ONLY revoked_token
    ADD CONSTRAINT constraint_rt PRIMARY KEY (id);

ALTER TABLE ONLY user_attribute
    ADD CONSTRAINT constraint_user_attribute_pk PRIMARY KEY (id);

ALTER TABLE ONLY user_group_membership
    ADD CONSTRAINT constraint_user_group PRIMARY KEY (group_id, user_id);

ALTER TABLE ONLY web_origins
    ADD CONSTRAINT constraint_web_origins PRIMARY KEY (client_id, value);

ALTER TABLE ONLY databasechangeloglock
    ADD CONSTRAINT databasechangeloglock_pkey PRIMARY KEY (id);

ALTER TABLE ONLY client_scope_attributes
    ADD CONSTRAINT pk_cl_tmpl_attr PRIMARY KEY (scope_id, name);

ALTER TABLE ONLY client_scope
    ADD CONSTRAINT pk_cli_template PRIMARY KEY (id);

ALTER TABLE ONLY resource_server
    ADD CONSTRAINT pk_resource_server PRIMARY KEY (id);

ALTER TABLE ONLY client_scope_role_mapping
    ADD CONSTRAINT pk_template_scope PRIMARY KEY (scope_id, role_id);

ALTER TABLE ONLY default_client_scope
    ADD CONSTRAINT r_def_cli_scope_bind PRIMARY KEY (realm_id, scope_id);

ALTER TABLE ONLY realm_localizations
    ADD CONSTRAINT realm_localizations_pkey PRIMARY KEY (realm_id, locale);

ALTER TABLE ONLY resource_attribute
    ADD CONSTRAINT res_attr_pk PRIMARY KEY (id);

ALTER TABLE ONLY keycloak_group
    ADD CONSTRAINT sibling_names UNIQUE (realm_id, parent_group, name);

ALTER TABLE ONLY identity_provider
    ADD CONSTRAINT uk_2daelwnibji49avxsrtuf6xj33 UNIQUE (provider_alias, realm_id);

ALTER TABLE ONLY client
    ADD CONSTRAINT uk_b71cjlbenv945rb6gcon438at UNIQUE (realm_id, client_id);

ALTER TABLE ONLY client_scope
    ADD CONSTRAINT uk_cli_scope UNIQUE (realm_id, name);

ALTER TABLE ONLY user_entity
    ADD CONSTRAINT uk_dykn684sl8up1crfei6eckhd7 UNIQUE (realm_id, email_constraint);

ALTER TABLE ONLY user_consent
    ADD CONSTRAINT uk_external_consent UNIQUE (client_storage_provider, external_client_id, user_id);

ALTER TABLE ONLY resource_server_resource
    ADD CONSTRAINT uk_frsr6t700s9v50bu18ws5ha6 UNIQUE (name, owner, resource_server_id);

ALTER TABLE ONLY resource_server_perm_ticket
    ADD CONSTRAINT uk_frsr6t700s9v50bu18ws5pmt UNIQUE (owner, requester, resource_server_id, resource_id, scope_id);

ALTER TABLE ONLY resource_server_policy
    ADD CONSTRAINT uk_frsrpt700s9v50bu18ws5ha6 UNIQUE (name, resource_server_id);

ALTER TABLE ONLY resource_server_scope
    ADD CONSTRAINT uk_frsrst700s9v50bu18ws5ha6 UNIQUE (name, resource_server_id);

ALTER TABLE ONLY user_consent
    ADD CONSTRAINT uk_local_consent UNIQUE (client_id, user_id);

ALTER TABLE ONLY org
    ADD CONSTRAINT uk_org_alias UNIQUE (realm_id, alias);

ALTER TABLE ONLY org
    ADD CONSTRAINT uk_org_group UNIQUE (group_id);

ALTER TABLE ONLY org
    ADD CONSTRAINT uk_org_name UNIQUE (realm_id, name);

ALTER TABLE ONLY realm
    ADD CONSTRAINT uk_orvsdmla56612eaefiq6wl5oi UNIQUE (name);

ALTER TABLE ONLY user_entity
    ADD CONSTRAINT uk_ru8tt6t700s9v50bu18ws5ha6 UNIQUE (realm_id, username);

CREATE INDEX fed_user_attr_long_values ON fed_user_attribute USING btree (long_value_hash, name);

CREATE INDEX fed_user_attr_long_values_lower_case ON fed_user_attribute USING btree (long_value_hash_lower_case, name);

CREATE INDEX idx_admin_event_time ON admin_event_entity USING btree (realm_id, admin_event_time);

CREATE INDEX idx_assoc_pol_assoc_pol_id ON associated_policy USING btree (associated_policy_id);

CREATE INDEX idx_auth_config_realm ON authenticator_config USING btree (realm_id);

CREATE INDEX idx_auth_exec_flow ON authentication_execution USING btree (flow_id);

CREATE INDEX idx_auth_exec_realm_flow ON authentication_execution USING btree (realm_id, flow_id);

CREATE INDEX idx_auth_flow_realm ON authentication_flow USING btree (realm_id);

CREATE INDEX idx_cl_clscope ON client_scope_client USING btree (scope_id);

CREATE INDEX idx_client_att_by_name_value ON client_attributes USING btree (name, substr(value, 1, 255));

CREATE INDEX idx_client_id ON client USING btree (client_id);

CREATE INDEX idx_client_init_acc_realm ON client_initial_access USING btree (realm_id);

CREATE INDEX idx_clscope_attrs ON client_scope_attributes USING btree (scope_id);

CREATE INDEX idx_clscope_cl ON client_scope_client USING btree (client_id);

CREATE INDEX idx_clscope_protmap ON protocol_mapper USING btree (client_scope_id);

CREATE INDEX idx_clscope_role ON client_scope_role_mapping USING btree (scope_id);

CREATE INDEX idx_compo_config_compo ON component_config USING btree (component_id);

CREATE INDEX idx_component_provider_type ON component USING btree (provider_type);

CREATE INDEX idx_component_realm ON component USING btree (realm_id);

CREATE INDEX idx_composite ON composite_role USING btree (composite);

CREATE INDEX idx_composite_child ON composite_role USING btree (child_role);

CREATE INDEX idx_defcls_realm ON default_client_scope USING btree (realm_id);

CREATE INDEX idx_defcls_scope ON default_client_scope USING btree (scope_id);

CREATE INDEX idx_event_time ON event_entity USING btree (realm_id, event_time);

CREATE INDEX idx_fedidentity_feduser ON federated_identity USING btree (federated_user_id);

CREATE INDEX idx_fedidentity_user ON federated_identity USING btree (user_id);

CREATE INDEX idx_fu_attribute ON fed_user_attribute USING btree (user_id, realm_id, name);

CREATE INDEX idx_fu_cnsnt_ext ON fed_user_consent USING btree (user_id, client_storage_provider, external_client_id);

CREATE INDEX idx_fu_consent ON fed_user_consent USING btree (user_id, client_id);

CREATE INDEX idx_fu_consent_ru ON fed_user_consent USING btree (realm_id, user_id);

CREATE INDEX idx_fu_credential ON fed_user_credential USING btree (user_id, type);

CREATE INDEX idx_fu_credential_ru ON fed_user_credential USING btree (realm_id, user_id);

CREATE INDEX idx_fu_group_membership ON fed_user_group_membership USING btree (user_id, group_id);

CREATE INDEX idx_fu_group_membership_ru ON fed_user_group_membership USING btree (realm_id, user_id);

CREATE INDEX idx_fu_required_action ON fed_user_required_action USING btree (user_id, required_action);

CREATE INDEX idx_fu_required_action_ru ON fed_user_required_action USING btree (realm_id, user_id);

CREATE INDEX idx_fu_role_mapping ON fed_user_role_mapping USING btree (user_id, role_id);

CREATE INDEX idx_fu_role_mapping_ru ON fed_user_role_mapping USING btree (realm_id, user_id);

CREATE INDEX idx_group_att_by_name_value ON group_attribute USING btree (name, ((value)::character varying(250)));

CREATE INDEX idx_group_attr_group ON group_attribute USING btree (group_id);

CREATE INDEX idx_group_role_mapp_group ON group_role_mapping USING btree (group_id);

CREATE INDEX idx_id_prov_mapp_realm ON identity_provider_mapper USING btree (realm_id);

CREATE INDEX idx_ident_prov_realm ON identity_provider USING btree (realm_id);

CREATE INDEX idx_idp_for_login ON identity_provider USING btree (realm_id, enabled, link_only, hide_on_login, organization_id);

CREATE INDEX idx_idp_realm_org ON identity_provider USING btree (realm_id, organization_id);

CREATE INDEX idx_keycloak_role_client ON keycloak_role USING btree (client);

CREATE INDEX idx_keycloak_role_realm ON keycloak_role USING btree (realm);

CREATE INDEX idx_offline_uss_by_broker_session_id ON offline_user_session USING btree (broker_session_id, realm_id);

CREATE INDEX idx_offline_uss_by_last_session_refresh ON offline_user_session USING btree (realm_id, offline_flag, last_session_refresh);

CREATE INDEX idx_offline_uss_by_user ON offline_user_session USING btree (user_id, realm_id, offline_flag);

CREATE INDEX idx_org_domain_org_id ON org_domain USING btree (org_id);

CREATE INDEX idx_perm_ticket_owner ON resource_server_perm_ticket USING btree (owner);

CREATE INDEX idx_perm_ticket_requester ON resource_server_perm_ticket USING btree (requester);

CREATE INDEX idx_protocol_mapper_client ON protocol_mapper USING btree (client_id);

CREATE INDEX idx_realm_attr_realm ON realm_attribute USING btree (realm_id);

CREATE INDEX idx_realm_clscope ON client_scope USING btree (realm_id);

CREATE INDEX idx_realm_def_grp_realm ON realm_default_groups USING btree (realm_id);

CREATE INDEX idx_realm_evt_list_realm ON realm_events_listeners USING btree (realm_id);

CREATE INDEX idx_realm_evt_types_realm ON realm_enabled_event_types USING btree (realm_id);

CREATE INDEX idx_realm_master_adm_cli ON realm USING btree (master_admin_client);

CREATE INDEX idx_realm_supp_local_realm ON realm_supported_locales USING btree (realm_id);

CREATE INDEX idx_redir_uri_client ON redirect_uris USING btree (client_id);

CREATE INDEX idx_req_act_prov_realm ON required_action_provider USING btree (realm_id);

CREATE INDEX idx_res_policy_policy ON resource_policy USING btree (policy_id);

CREATE INDEX idx_res_scope_scope ON resource_scope USING btree (scope_id);

CREATE INDEX idx_res_serv_pol_res_serv ON resource_server_policy USING btree (resource_server_id);

CREATE INDEX idx_res_srv_res_res_srv ON resource_server_resource USING btree (resource_server_id);

CREATE INDEX idx_res_srv_scope_res_srv ON resource_server_scope USING btree (resource_server_id);

CREATE INDEX idx_rev_token_on_expire ON revoked_token USING btree (expire);

CREATE INDEX idx_role_attribute ON role_attribute USING btree (role_id);

CREATE INDEX idx_role_clscope ON client_scope_role_mapping USING btree (role_id);

CREATE INDEX idx_scope_mapping_role ON scope_mapping USING btree (role_id);

CREATE INDEX idx_scope_policy_policy ON scope_policy USING btree (policy_id);

CREATE INDEX idx_update_time ON migration_model USING btree (update_time);

CREATE INDEX idx_usconsent_clscope ON user_consent_client_scope USING btree (user_consent_id);

CREATE INDEX idx_usconsent_scope_id ON user_consent_client_scope USING btree (scope_id);

CREATE INDEX idx_user_attribute ON user_attribute USING btree (user_id);

CREATE INDEX idx_user_attribute_name ON user_attribute USING btree (name, value);

CREATE INDEX idx_user_consent ON user_consent USING btree (user_id);

CREATE INDEX idx_user_credential ON credential USING btree (user_id);

CREATE INDEX idx_user_email ON user_entity USING btree (email);

CREATE INDEX idx_user_group_mapping ON user_group_membership USING btree (user_id);

CREATE INDEX idx_user_reqactions ON user_required_action USING btree (user_id);

CREATE INDEX idx_user_role_mapping ON user_role_mapping USING btree (user_id);

CREATE INDEX idx_user_service_account ON user_entity USING btree (realm_id, service_account_client_link);

CREATE INDEX idx_usr_fed_map_fed_prv ON user_federation_mapper USING btree (federation_provider_id);

CREATE INDEX idx_usr_fed_map_realm ON user_federation_mapper USING btree (realm_id);

CREATE INDEX idx_usr_fed_prv_realm ON user_federation_provider USING btree (realm_id);

CREATE INDEX idx_web_orig_client ON web_origins USING btree (client_id);

CREATE INDEX user_attr_long_values ON user_attribute USING btree (long_value_hash, name);

CREATE INDEX user_attr_long_values_lower_case ON user_attribute USING btree (long_value_hash_lower_case, name);

ALTER TABLE ONLY identity_provider
    ADD CONSTRAINT fk2b4ebc52ae5c3b34 FOREIGN KEY (realm_id) REFERENCES realm(id);

ALTER TABLE ONLY client_attributes
    ADD CONSTRAINT fk3c47c64beacca966 FOREIGN KEY (client_id) REFERENCES client(id);

ALTER TABLE ONLY federated_identity
    ADD CONSTRAINT fk404288b92ef007a6 FOREIGN KEY (user_id) REFERENCES user_entity(id);

ALTER TABLE ONLY client_node_registrations
    ADD CONSTRAINT fk4129723ba992f594 FOREIGN KEY (client_id) REFERENCES client(id);

ALTER TABLE ONLY redirect_uris
    ADD CONSTRAINT fk_1burs8pb4ouj97h5wuppahv9f FOREIGN KEY (client_id) REFERENCES client(id);

ALTER TABLE ONLY user_federation_provider
    ADD CONSTRAINT fk_1fj32f6ptolw2qy60cd8n01e8 FOREIGN KEY (realm_id) REFERENCES realm(id);

ALTER TABLE ONLY realm_required_credential
    ADD CONSTRAINT fk_5hg65lybevavkqfki3kponh9v FOREIGN KEY (realm_id) REFERENCES realm(id);

ALTER TABLE ONLY resource_attribute
    ADD CONSTRAINT fk_5hrm2vlf9ql5fu022kqepovbr FOREIGN KEY (resource_id) REFERENCES resource_server_resource(id);

ALTER TABLE ONLY user_attribute
    ADD CONSTRAINT fk_5hrm2vlf9ql5fu043kqepovbr FOREIGN KEY (user_id) REFERENCES user_entity(id);

ALTER TABLE ONLY user_required_action
    ADD CONSTRAINT fk_6qj3w1jw9cvafhe19bwsiuvmd FOREIGN KEY (user_id) REFERENCES user_entity(id);

ALTER TABLE ONLY keycloak_role
    ADD CONSTRAINT fk_6vyqfe4cn4wlq8r6kt5vdsj5c FOREIGN KEY (realm) REFERENCES realm(id);

ALTER TABLE ONLY realm_smtp_config
    ADD CONSTRAINT fk_70ej8xdxgxd0b9hh6180irr0o FOREIGN KEY (realm_id) REFERENCES realm(id);

ALTER TABLE ONLY realm_attribute
    ADD CONSTRAINT fk_8shxd6l3e9atqukacxgpffptw FOREIGN KEY (realm_id) REFERENCES realm(id);

ALTER TABLE ONLY composite_role
    ADD CONSTRAINT fk_a63wvekftu8jo1pnj81e7mce2 FOREIGN KEY (composite) REFERENCES keycloak_role(id);

ALTER TABLE ONLY authentication_execution
    ADD CONSTRAINT fk_auth_exec_flow FOREIGN KEY (flow_id) REFERENCES authentication_flow(id);

ALTER TABLE ONLY authentication_execution
    ADD CONSTRAINT fk_auth_exec_realm FOREIGN KEY (realm_id) REFERENCES realm(id);

ALTER TABLE ONLY authentication_flow
    ADD CONSTRAINT fk_auth_flow_realm FOREIGN KEY (realm_id) REFERENCES realm(id);

ALTER TABLE ONLY authenticator_config
    ADD CONSTRAINT fk_auth_realm FOREIGN KEY (realm_id) REFERENCES realm(id);

ALTER TABLE ONLY user_role_mapping
    ADD CONSTRAINT fk_c4fqv34p1mbylloxang7b1q3l FOREIGN KEY (user_id) REFERENCES user_entity(id);

ALTER TABLE ONLY client_scope_attributes
    ADD CONSTRAINT fk_cl_scope_attr_scope FOREIGN KEY (scope_id) REFERENCES client_scope(id);

ALTER TABLE ONLY client_scope_role_mapping
    ADD CONSTRAINT fk_cl_scope_rm_scope FOREIGN KEY (scope_id) REFERENCES client_scope(id);

ALTER TABLE ONLY protocol_mapper
    ADD CONSTRAINT fk_cli_scope_mapper FOREIGN KEY (client_scope_id) REFERENCES client_scope(id);

ALTER TABLE ONLY client_initial_access
    ADD CONSTRAINT fk_client_init_acc_realm FOREIGN KEY (realm_id) REFERENCES realm(id);

ALTER TABLE ONLY component_config
    ADD CONSTRAINT fk_component_config FOREIGN KEY (component_id) REFERENCES component(id);

ALTER TABLE ONLY component
    ADD CONSTRAINT fk_component_realm FOREIGN KEY (realm_id) REFERENCES realm(id);

ALTER TABLE ONLY realm_default_groups
    ADD CONSTRAINT fk_def_groups_realm FOREIGN KEY (realm_id) REFERENCES realm(id);

ALTER TABLE ONLY user_federation_mapper_config
    ADD CONSTRAINT fk_fedmapper_cfg FOREIGN KEY (user_federation_mapper_id) REFERENCES user_federation_mapper(id);

ALTER TABLE ONLY user_federation_mapper
    ADD CONSTRAINT fk_fedmapperpm_fedprv FOREIGN KEY (federation_provider_id) REFERENCES user_federation_provider(id);

ALTER TABLE ONLY user_federation_mapper
    ADD CONSTRAINT fk_fedmapperpm_realm FOREIGN KEY (realm_id) REFERENCES realm(id);

ALTER TABLE ONLY associated_policy
    ADD CONSTRAINT fk_frsr5s213xcx4wnkog82ssrfy FOREIGN KEY (associated_policy_id) REFERENCES resource_server_policy(id);

ALTER TABLE ONLY scope_policy
    ADD CONSTRAINT fk_frsrasp13xcx4wnkog82ssrfy FOREIGN KEY (policy_id) REFERENCES resource_server_policy(id);

ALTER TABLE ONLY resource_server_perm_ticket
    ADD CONSTRAINT fk_frsrho213xcx4wnkog82sspmt FOREIGN KEY (resource_server_id) REFERENCES resource_server(id);

ALTER TABLE ONLY resource_server_resource
    ADD CONSTRAINT fk_frsrho213xcx4wnkog82ssrfy FOREIGN KEY (resource_server_id) REFERENCES resource_server(id);

ALTER TABLE ONLY resource_server_perm_ticket
    ADD CONSTRAINT fk_frsrho213xcx4wnkog83sspmt FOREIGN KEY (resource_id) REFERENCES resource_server_resource(id);

ALTER TABLE ONLY resource_server_perm_ticket
    ADD CONSTRAINT fk_frsrho213xcx4wnkog84sspmt FOREIGN KEY (scope_id) REFERENCES resource_server_scope(id);

ALTER TABLE ONLY associated_policy
    ADD CONSTRAINT fk_frsrpas14xcx4wnkog82ssrfy FOREIGN KEY (policy_id) REFERENCES resource_server_policy(id);

ALTER TABLE ONLY scope_policy
    ADD CONSTRAINT fk_frsrpass3xcx4wnkog82ssrfy FOREIGN KEY (scope_id) REFERENCES resource_server_scope(id);

ALTER TABLE ONLY resource_server_perm_ticket
    ADD CONSTRAINT fk_frsrpo2128cx4wnkog82ssrfy FOREIGN KEY (policy_id) REFERENCES resource_server_policy(id);

ALTER TABLE ONLY resource_server_policy
    ADD CONSTRAINT fk_frsrpo213xcx4wnkog82ssrfy FOREIGN KEY (resource_server_id) REFERENCES resource_server(id);

ALTER TABLE ONLY resource_scope
    ADD CONSTRAINT fk_frsrpos13xcx4wnkog82ssrfy FOREIGN KEY (resource_id) REFERENCES resource_server_resource(id);

ALTER TABLE ONLY resource_policy
    ADD CONSTRAINT fk_frsrpos53xcx4wnkog82ssrfy FOREIGN KEY (resource_id) REFERENCES resource_server_resource(id);

ALTER TABLE ONLY resource_policy
    ADD CONSTRAINT fk_frsrpp213xcx4wnkog82ssrfy FOREIGN KEY (policy_id) REFERENCES resource_server_policy(id);

ALTER TABLE ONLY resource_scope
    ADD CONSTRAINT fk_frsrps213xcx4wnkog82ssrfy FOREIGN KEY (scope_id) REFERENCES resource_server_scope(id);

ALTER TABLE ONLY resource_server_scope
    ADD CONSTRAINT fk_frsrso213xcx4wnkog82ssrfy FOREIGN KEY (resource_server_id) REFERENCES resource_server(id);

ALTER TABLE ONLY composite_role
    ADD CONSTRAINT fk_gr7thllb9lu8q4vqa4524jjy8 FOREIGN KEY (child_role) REFERENCES keycloak_role(id);

ALTER TABLE ONLY user_consent_client_scope
    ADD CONSTRAINT fk_grntcsnt_clsc_usc FOREIGN KEY (user_consent_id) REFERENCES user_consent(id);

ALTER TABLE ONLY user_consent
    ADD CONSTRAINT fk_grntcsnt_user FOREIGN KEY (user_id) REFERENCES user_entity(id);

ALTER TABLE ONLY group_attribute
    ADD CONSTRAINT fk_group_attribute_group FOREIGN KEY (group_id) REFERENCES keycloak_group(id);

ALTER TABLE ONLY group_role_mapping
    ADD CONSTRAINT fk_group_role_group FOREIGN KEY (group_id) REFERENCES keycloak_group(id);

ALTER TABLE ONLY realm_enabled_event_types
    ADD CONSTRAINT fk_h846o4h0w8epx5nwedrf5y69j FOREIGN KEY (realm_id) REFERENCES realm(id);

ALTER TABLE ONLY realm_events_listeners
    ADD CONSTRAINT fk_h846o4h0w8epx5nxev9f5y69j FOREIGN KEY (realm_id) REFERENCES realm(id);

ALTER TABLE ONLY identity_provider_mapper
    ADD CONSTRAINT fk_idpm_realm FOREIGN KEY (realm_id) REFERENCES realm(id);

ALTER TABLE ONLY idp_mapper_config
    ADD CONSTRAINT fk_idpmconfig FOREIGN KEY (idp_mapper_id) REFERENCES identity_provider_mapper(id);

ALTER TABLE ONLY web_origins
    ADD CONSTRAINT fk_lojpho213xcx4wnkog82ssrfy FOREIGN KEY (client_id) REFERENCES client(id);

ALTER TABLE ONLY scope_mapping
    ADD CONSTRAINT fk_ouse064plmlr732lxjcn1q5f1 FOREIGN KEY (client_id) REFERENCES client(id);

ALTER TABLE ONLY protocol_mapper
    ADD CONSTRAINT fk_pcm_realm FOREIGN KEY (client_id) REFERENCES client(id);

ALTER TABLE ONLY credential
    ADD CONSTRAINT fk_pfyr0glasqyl0dei3kl69r6v0 FOREIGN KEY (user_id) REFERENCES user_entity(id);

ALTER TABLE ONLY protocol_mapper_config
    ADD CONSTRAINT fk_pmconfig FOREIGN KEY (protocol_mapper_id) REFERENCES protocol_mapper(id);

ALTER TABLE ONLY default_client_scope
    ADD CONSTRAINT fk_r_def_cli_scope_realm FOREIGN KEY (realm_id) REFERENCES realm(id);

ALTER TABLE ONLY required_action_provider
    ADD CONSTRAINT fk_req_act_realm FOREIGN KEY (realm_id) REFERENCES realm(id);

ALTER TABLE ONLY resource_uris
    ADD CONSTRAINT fk_resource_server_uris FOREIGN KEY (resource_id) REFERENCES resource_server_resource(id);

ALTER TABLE ONLY role_attribute
    ADD CONSTRAINT fk_role_attribute_id FOREIGN KEY (role_id) REFERENCES keycloak_role(id);

ALTER TABLE ONLY realm_supported_locales
    ADD CONSTRAINT fk_supported_locales_realm FOREIGN KEY (realm_id) REFERENCES realm(id);

ALTER TABLE ONLY user_federation_config
    ADD CONSTRAINT fk_t13hpu1j94r2ebpekr39x5eu5 FOREIGN KEY (user_federation_provider_id) REFERENCES user_federation_provider(id);

ALTER TABLE ONLY user_group_membership
    ADD CONSTRAINT fk_user_group_user FOREIGN KEY (user_id) REFERENCES user_entity(id);

ALTER TABLE ONLY policy_config
    ADD CONSTRAINT fkdc34197cf864c4e43 FOREIGN KEY (policy_id) REFERENCES resource_server_policy(id);

ALTER TABLE ONLY identity_provider_config
    ADD CONSTRAINT fkdc4897cf864c4e43 FOREIGN KEY (identity_provider_id) REFERENCES identity_provider(internal_id);

