-- auto-generated definition
create table users
(
  id                       serial                  not null
    constraint users_pkey
    primary key,
  uid                      varchar(36)             not null,
  username                 varchar(50)             not null,
  password                 varchar(255)            not null,
  activated                boolean default false   not null,
  activation_code          varchar(50)             not null,
  activation_code_validity timestamp               not null,
  deleted                  boolean default false   not null,
  created_at               timestamp default now() not null
);

alter table users
  owner to postgres;

create unique index users_id_uindex
  on users (id);

create unique index users_id_uindex_2
  on users (id);

create unique index users_uid_uindex
  on users (uid);

create unique index users_username_uindex
  on users (username);

