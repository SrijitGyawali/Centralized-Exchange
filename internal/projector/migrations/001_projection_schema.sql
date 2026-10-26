-- The SQL read model (ADR-0009). Everything here is DERIVED from the journal
-- and can be dropped and rebuilt at any time. The exchange never reads it to
-- make a decision: risk checks, balances and dedupe live in the engine.
--
-- Conventions:
--   * Money and quantities are BIGINT in the smallest unit (USDT atoms,
--     BTC lots, price ticks), exactly as in the engine. Never NUMERIC/FLOAT.
--   * Every row is keyed by its identity in the journal (command Seq plus a
--     position), so writing the same command twice violates a primary key
--     instead of silently duplicating money.
--   * Enumerations are lower-case text with CHECK constraints: readable in
--     psql, still validated.

-- Exactly one row: the last journal Seq whose effects are fully in these
-- tables. It is updated in the SAME transaction as the rows it describes.
-- last_user_id/last_key identify the command AT that Seq, so a projector
-- started against a DIFFERENT journal (same length or longer) notices that
-- the histories differ instead of silently mixing them.
create table projection_checkpoint (
    id           smallint primary key default 1 check (id = 1),
    seq          bigint   not null check (seq >= 0),
    last_user_id bigint   not null default 0,
    last_key     text     not null default ''
);
insert into projection_checkpoint (seq) values (0);

-- Every sequenced command, including rejections and duplicates.
create table commands (
    seq       bigint  primary key check (seq > 0),
    user_id   bigint  not null,
    idem_key  text    not null,
    kind      text    not null check (kind in ('fund', 'place', 'cancel')),
    accepted  boolean not null,
    rejected  text    not null, -- '' when accepted; else the reason
    duplicate boolean not null  -- a retried key: its effects were recorded the first time
);
create index commands_by_user on commands (user_id, seq);

-- One row per order ever accepted, with its whole lifecycle.
create table orders (
    order_id      bigint primary key check (order_id > 0),
    user_id       bigint not null,
    side          text   not null check (side in ('buy', 'sell')),
    time_in_force text   not null check (time_in_force in ('gtc', 'ioc')),
    price_ticks   bigint not null check (price_ticks > 0),
    qty_lots      bigint not null check (qty_lots > 0),
    filled_lots   bigint not null check (filled_lots >= 0),
    open_lots     bigint not null check (open_lots >= 0),
    status        text   not null check (status in
                    ('open', 'partially_filled', 'filled', 'canceled',
                     'canceled_ioc', 'canceled_self_trade')),
    placed_seq    bigint not null references commands (seq),
    updated_seq   bigint not null,
    check (filled_lots + open_lots <= qty_lots)
);
create index orders_by_user on orders (user_id, order_id);

-- One row per fill. A fill always executes at the maker's price.
create table trades (
    seq            bigint not null references commands (seq),
    fill_index     int    not null check (fill_index >= 0),
    taker_order_id bigint not null references orders (order_id),
    maker_order_id bigint not null references orders (order_id),
    taker_user_id  bigint not null,
    maker_user_id  bigint not null,
    taker_side     text   not null check (taker_side in ('buy', 'sell')),
    price_ticks    bigint not null check (price_ticks > 0),
    qty_lots       bigint not null check (qty_lots > 0),
    primary key (seq, fill_index)
);
create index trades_by_taker on trades (taker_user_id, seq);
create index trades_by_maker on trades (maker_user_id, seq);

-- The double-entry ledger, exactly as the engine posted it.
create table ledger_journals (
    seq            bigint not null references commands (seq),
    journal_index  int    not null check (journal_index >= 0),
    kind           text   not null check (kind in ('funding', 'reservation', 'settlement', 'release')),
    order_id       bigint not null, -- 0 for funding
    maker_order_id bigint not null, -- settlements only, else 0
    primary key (seq, journal_index)
);

create table ledger_lines (
    seq           bigint not null,
    journal_index int    not null,
    line_index    int    not null check (line_index >= 0),
    user_id       bigint not null, -- 0 = the system (issuance)
    bucket        text   not null check (bucket in ('available', 'reserved', 'issuance')),
    asset         text   not null check (asset in ('BTC', 'USDT')),
    amount        bigint not null check (amount <> 0),
    primary key (seq, journal_index, line_index),
    foreign key (seq, journal_index) references ledger_journals (seq, journal_index)
);
create index ledger_lines_by_user on ledger_lines (user_id, asset, seq);

-- Running balances: the sum of every ledger line per account. Kept up to
-- date in the same transaction, so queries need not sum the whole ledger.
create table account_balances (
    user_id bigint not null,
    asset   text   not null check (asset in ('BTC', 'USDT')),
    bucket  text   not null check (bucket in ('available', 'reserved', 'issuance')),
    amount  bigint not null,
    primary key (user_id, asset, bucket)
);

---- create above / drop below ----

drop table account_balances;
drop table ledger_lines;
drop table ledger_journals;
drop table trades;
drop table orders;
drop table commands;
drop table projection_checkpoint;
