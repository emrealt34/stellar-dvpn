//! # dmesh Settlement Contract
//!
//! Trustless bandwidth settlement on Stellar Soroban. The contract holds no
//! funds and no keys: it only verifies dual-party authorization, enforces the
//! contract-stored tier prices and the immutable 20% protocol fee, and pulls
//! payment from the client's token allowance at settlement time.
//!
//! ## Flow
//! 1. Client approves a token allowance for the contract.
//! 2. `open_session` — client authorizes a bounded settlement session.
//! 3. `propose_settlement` — client commits signed (Stellar-auth) byte totals.
//! 4. `confirm_settlement` — node authorizes the identical totals; the
//!    contract splits payment 80% node / 20% protocol via `transfer_from`.
//! 5. Mismatch, replay or expiry all fail closed: no allowance is ever drawn.

#![no_std]

use soroban_sdk::{
    contract, contracterror, contractimpl, contracttype, panic_with_error, Address, BytesN, Env,
    Map, Symbol,
};

/// Scaled integer constants.
const SCALE: i128 = 1_000_000; // micro-USD
const STROOPS_PER_TOKEN: i128 = 10_000_000; // Stellar's 7-decimal unit
const FEE_DENOMINATOR: u32 = 10_000;
const BYTES_PER_GB: i128 = 1_000_000_000;

#[contracterror]
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
#[repr(u32)]
pub enum ContractError {
    AlreadyInitialized = 1,
    NotAdmin = 2,
    UnknownTier = 3,
    UnknownAssetPrice = 4,
    SessionNotFound = 5,
    AlreadySettled = 6,
    SessionExpired = 7,
    ProposalMismatch = 8,
    NoProposal = 9,
    InvalidAmount = 10,
    InvalidFeeBps = 11,
    AlreadyCancelled = 12,
    ZeroTtl = 13,
}

#[contracttype]
#[derive(Clone, Debug)]
pub struct Session {
    pub client: Address,
    pub node: Address,
    pub token: Address,
    pub tier: Symbol,
    pub nonce: BytesN<32>,
    pub expires_at: u64,
    pub proposed: bool,
    pub proposed_up: u64,
    pub proposed_down: u64,
    pub settled: bool,
    pub cancelled: bool,
}

#[contracttype]
#[derive(Clone, Debug)]
pub struct SettlementEvent {
    pub session_id: u128,
    pub client: Address,
    pub node: Address,
    pub token: Address,
    pub up_bytes: u64,
    pub down_bytes: u64,
    pub total_stroops: i128,
    pub node_stroops: i128,
    pub fee_stroops: i128,
}

const ADMIN: &str = "Admin";
const FEE_BPS: &str = "FeeBps";
const COUNTER: &str = "Counter";
const SESSIONS: &str = "Sessions";
const TIERS: &str = "Tiers";
const PRICES: &str = "Prices";

#[contract]
pub struct DMeshSettlement;

#[contractimpl]
impl DMeshSettlement {
    /// One-time initialization. `fee_bps` is the protocol fee in basis points
    /// (2000 = 20%).
    pub fn initialize(env: Env, admin: Address, fee_bps: u32) {
        if env.storage().persistent().has(&Symbol::new(&env, ADMIN)) {
            panic_with_error!(&env, ContractError::AlreadyInitialized);
        }
        if fee_bps > FEE_DENOMINATOR {
            panic_with_error!(&env, ContractError::InvalidFeeBps);
        }
        admin.require_auth();
        env.storage().persistent().set(&Symbol::new(&env, ADMIN), &admin);
        env.storage().persistent().set(&Symbol::new(&env, FEE_BPS), &fee_bps);
        env.storage().persistent().set(&Symbol::new(&env, COUNTER), &0u128);
        env.storage().persistent().set(
            &Symbol::new(&env, TIERS),
            &Map::<Symbol, i128>::new(&env),
        );
        env.storage().persistent().set(
            &Symbol::new(&env, PRICES),
            &Map::<Address, i128>::new(&env),
        );
        env.storage().persistent().set(
            &Symbol::new(&env, SESSIONS),
            &Map::<u128, Session>::new(&env),
        );
        env.events().publish((Symbol::new(&env, "init"),), fee_bps);
    }

    /// Admin sets/updates a tier price in micro-USD per GB. Every change emits
    /// an event, making rate history permanently auditable.
    pub fn set_tier(env: Env, tier: Symbol, price_micro_usd_per_gb: i128) {
        let admin: Address = env.storage().persistent().get(&Symbol::new(&env, ADMIN)).unwrap();
        admin.require_auth();
        if price_micro_usd_per_gb <= 0 {
            panic_with_error!(&env, ContractError::InvalidAmount);
        }
        let mut tiers: Map<Symbol, i128> = env
            .storage()
            .persistent()
            .get(&Symbol::new(&env, TIERS))
            .unwrap();
        tiers.set(tier.clone(), price_micro_usd_per_gb);
        env.storage().persistent().set(&Symbol::new(&env, TIERS), &tiers);
        env.events().publish((Symbol::new(&env, "tier_set"), tier), price_micro_usd_per_gb);
    }

    /// Admin/oracle sets the USD price of one whole token (micro-USD). Refreshed
    /// daily by CI for every supported asset.
    pub fn set_asset_price(env: Env, token: Address, micro_usd_per_token: i128) {
        let admin: Address = env.storage().persistent().get(&Symbol::new(&env, ADMIN)).unwrap();
        admin.require_auth();
        if micro_usd_per_token <= 0 {
            panic_with_error!(&env, ContractError::InvalidAmount);
        }
        let mut prices: Map<Address, i128> = env
            .storage()
            .persistent()
            .get(&Symbol::new(&env, PRICES))
            .unwrap();
        prices.set(token.clone(), micro_usd_per_token);
        env.storage().persistent().set(&Symbol::new(&env, PRICES), &prices);
        env.events()
            .publish((Symbol::new(&env, "price_set"), token), micro_usd_per_token);
    }

    /// Client opens a settlement session. No funds move: the client separately
    /// approves an allowance for this contract on the token contract.
    pub fn open_session(
        env: Env,
        client: Address,
        node: Address,
        token: Address,
        tier: Symbol,
        nonce: BytesN<32>,
        ttl_seconds: u64,
    ) -> u128 {
        client.require_auth();
        if ttl_seconds == 0 {
            panic_with_error!(&env, ContractError::ZeroTtl);
        }
        let tiers: Map<Symbol, i128> = env
            .storage()
            .persistent()
            .get(&Symbol::new(&env, TIERS))
            .unwrap();
        if !tiers.contains_key(tier.clone()) {
            panic_with_error!(&env, ContractError::UnknownTier);
        }
        let prices: Map<Address, i128> = env
            .storage()
            .persistent()
            .get(&Symbol::new(&env, PRICES))
            .unwrap();
        if !prices.contains_key(token.clone()) {
            panic_with_error!(&env, ContractError::UnknownAssetPrice);
        }

        let counter: u128 = env.storage().persistent().get(&Symbol::new(&env, COUNTER)).unwrap();
        let session_id = counter + 1;
        let session = Session {
            client: client.clone(),
            node: node.clone(),
            token: token.clone(),
            tier: tier.clone(),
            nonce: nonce.clone(),
            expires_at: env.ledger().timestamp() + ttl_seconds,
            proposed: false,
            proposed_up: 0,
            proposed_down: 0,
            settled: false,
            cancelled: false,
        };
        let mut sessions: Map<u128, Session> = env
            .storage()
            .persistent()
            .get(&Symbol::new(&env, SESSIONS))
            .unwrap();
        sessions.set(session_id, session);
        env.storage().persistent().set(&Symbol::new(&env, SESSIONS), &sessions);
        env.storage().persistent().set(&Symbol::new(&env, COUNTER), &session_id);
        env.events().publish(
            (Symbol::new(&env, "opened"), session_id),
            (client, node, token, tier, nonce),
        );
        session_id
    }

    /// Client commits the cumulative byte counters it agrees to pay.
    pub fn propose_settlement(env: Env, session_id: u128, up_bytes: u64, down_bytes: u64) {
        let mut sessions: Map<u128, Session> = env
            .storage()
            .persistent()
            .get(&Symbol::new(&env, SESSIONS))
            .unwrap();
        let mut session = load_session(&env, &sessions, session_id);
        session.client.require_auth();
        if session.settled {
            panic_with_error!(&env, ContractError::AlreadySettled);
        }
        if session.cancelled {
            panic_with_error!(&env, ContractError::AlreadyCancelled);
        }
        if env.ledger().timestamp() > session.expires_at {
            panic_with_error!(&env, ContractError::SessionExpired);
        }
        if session.proposed && (session.proposed_up != up_bytes || session.proposed_down != down_bytes) {
            panic_with_error!(&env, ContractError::ProposalMismatch);
        }
        session.proposed = true;
        session.proposed_up = up_bytes;
        session.proposed_down = down_bytes;
        sessions.set(session_id, session.clone());
        env.storage().persistent().set(&Symbol::new(&env, SESSIONS), &sessions);
        env.events().publish(
            (Symbol::new(&env, "proposed"), session_id),
            (up_bytes, down_bytes),
        );
    }

    /// Node confirms the same counters; the contract computes the cost from
    /// on-chain tier and oracle data, then pulls 80/20 split payment from the
    /// client's allowance. The contract never custodies funds.
    pub fn confirm_settlement(env: Env, session_id: u128, up_bytes: u64, down_bytes: u64) {
        let mut sessions: Map<u128, Session> = env
            .storage()
            .persistent()
            .get(&Symbol::new(&env, SESSIONS))
            .unwrap();
        let mut session = load_session(&env, &sessions, session_id);
        session.node.require_auth();
        if session.settled {
            panic_with_error!(&env, ContractError::AlreadySettled);
        }
        if session.cancelled {
            panic_with_error!(&env, ContractError::AlreadyCancelled);
        }
        if env.ledger().timestamp() > session.expires_at {
            panic_with_error!(&env, ContractError::SessionExpired);
        }
        if !session.proposed {
            panic_with_error!(&env, ContractError::NoProposal);
        }
        if session.proposed_up != up_bytes || session.proposed_down != down_bytes {
            panic_with_error!(&env, ContractError::ProposalMismatch);
        }

        let tiers: Map<Symbol, i128> = env
            .storage()
            .persistent()
            .get(&Symbol::new(&env, TIERS))
            .unwrap();
        let tier_price = tiers.get(session.tier.clone()).unwrap_or_else(|| {
            panic_with_error!(&env, ContractError::UnknownTier);
            unreachable!()
        });
        let prices: Map<Address, i128> = env
            .storage()
            .persistent()
            .get(&Symbol::new(&env, PRICES))
            .unwrap();
        let token_price = prices.get(session.token.clone()).unwrap_or_else(|| {
            panic_with_error!(&env, ContractError::UnknownAssetPrice);
            unreachable!()
        });

        let total_stroops =
            compute_cost(up_bytes as i128 + down_bytes as i128, tier_price, token_price);
        if total_stroops <= 0 {
            panic_with_error!(&env, ContractError::InvalidAmount);
        }
        let fee_bps: u32 = env.storage().persistent().get(&Symbol::new(&env, FEE_BPS)).unwrap();
        let fee_stroops = total_stroops * fee_bps as i128 / FEE_DENOMINATOR as i128;
        let node_stroops = total_stroops - fee_stroops;

        let token_client = soroban_sdk::token::Client::new(&env, &session.token);
        let contract_addr = env.current_contract_address();
        // Spender is the contract itself, drawing against the client's
        // pre-authorized allowance.
        token_client.transfer_from(
            &contract_addr,
            &session.client,
            &session.node,
            &node_stroops,
        );
        let admin: Address = env.storage().persistent().get(&Symbol::new(&env, ADMIN)).unwrap();
        token_client.transfer_from(
            &contract_addr,
            &session.client,
            &admin,
            &fee_stroops,
        );

        session.settled = true;
        sessions.set(session_id, session.clone());
        env.storage().persistent().set(&Symbol::new(&env, SESSIONS), &sessions);
        env.events().publish(
            (Symbol::new(&env, "settled"), session_id),
            SettlementEvent {
                session_id,
                client: session.client,
                node: session.node,
                token: session.token,
                up_bytes,
                down_bytes,
                total_stroops,
                node_stroops,
                fee_stroops,
            },
        );
    }

    /// Either party may cancel an unsettled session; an expired session is
    /// likewise unchargeable. No allowance is ever drawn after cancellation.
    /// `as_node = false` authenticates as the client, `true` as the node.
    pub fn cancel_session(env: Env, session_id: u128, as_node: bool) {
        let mut sessions: Map<u128, Session> = env
            .storage()
            .persistent()
            .get(&Symbol::new(&env, SESSIONS))
            .unwrap();
        let mut session = load_session(&env, &sessions, session_id);
        if as_node {
            session.node.require_auth();
        } else {
            session.client.require_auth();
        }
        if session.settled {
            panic_with_error!(&env, ContractError::AlreadySettled);
        }
        session.cancelled = true;
        sessions.set(session_id, session);
        env.storage().persistent().set(&Symbol::new(&env, SESSIONS), &sessions);
        env.events().publish((Symbol::new(&env, "cancelled"), session_id), ());
    }

    // ---- Views ----

    pub fn get_session(env: Env, session_id: u128) -> Option<Session> {
        let sessions: Map<u128, Session> = env
            .storage()
            .persistent()
            .get(&Symbol::new(&env, SESSIONS))
            .unwrap();
        sessions.get(session_id)
    }

    pub fn get_tier(env: Env, tier: Symbol) -> Option<i128> {
        let tiers: Map<Symbol, i128> = env
            .storage()
            .persistent()
            .get(&Symbol::new(&env, TIERS))
            .unwrap();
        tiers.get(tier)
    }

    pub fn get_asset_price(env: Env, token: Address) -> Option<i128> {
        let prices: Map<Address, i128> = env
            .storage()
            .persistent()
            .get(&Symbol::new(&env, PRICES))
            .unwrap();
        prices.get(token)
    }

    pub fn fee_bps(env: Env) -> u32 {
        env.storage().persistent().get(&Symbol::new(&env, FEE_BPS)).unwrap()
    }

    /// Number of sessions ever opened. Because ids are assigned sequentially
    /// starting at 1, this is also the highest assigned session id.
    pub fn session_count(env: Env) -> u128 {
        env.storage()
            .persistent()
            .get(&Symbol::new(&env, COUNTER))
            .unwrap_or(0)
    }
}

/// Loads a session or fails the contract with `SessionNotFound`.
fn load_session(env: &Env, sessions: &Map<u128, Session>, session_id: u128) -> Session {
    match sessions.get(session_id) {
        Some(s) => s,
        None => panic_with_error!(env, ContractError::SessionNotFound),
    }
}

#[cfg(test)]
mod test;

/// Pure pricing math, unit-tested independently.
///
/// `bytes` total counters, `tier_micro_usd` per GB, `token_micro_usd` per whole
/// token. Result in stroops (1e-7 of a token).
pub fn compute_cost(bytes: i128, tier_micro_usd: i128, token_micro_usd: i128) -> i128 {
    // usd in micro units: bytes/1e9 * tier price
    let usd_micro = bytes * tier_micro_usd / BYTES_PER_GB;
    // stroops = micro-usd * 1e7 / micro-usd-per-token
    usd_micro * STROOPS_PER_TOKEN / token_micro_usd
}
