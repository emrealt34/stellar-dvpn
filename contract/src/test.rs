#![cfg(test)]

use super::*;
use soroban_sdk::{
    testutils::{Address as _, Ledger as _, MockAuth, MockAuthInvoke},
    token, Address, BytesN, Env, IntoVal, Symbol,
};

const RESIDENTIAL: i128 = 150_000; // $0.15/GB in micro-USD
const DATACENTER: i128 = 30_000; // $0.03/GB in micro-USD
const XLM_PRICE: i128 = 100_000; // $0.10 per XLM in micro-USD
const FEE_BPS: u32 = 2000; // 20%
const GB: i128 = 1_000_000_000;

struct TestCtx {
    env: Env,
    contract: Address,
    token: Address,
    admin: Address,
    client: Address,
    node: Address,
}

fn setup() -> TestCtx {
    let env = Env::default();
    env.mock_all_auths();

    let admin = Address::generate(&env);
    let client_user = Address::generate(&env);
    let node = Address::generate(&env);

    // A Stellar asset contract stands in for XLM's SAC in tests.
    let token_reg = env.register_stellar_asset_contract_v2(admin.clone());
    let token = token_reg.address();

    let contract = env.register(DMeshSettlement, ());
    let dc = DMeshSettlementClient::new(&env, &contract);
    dc.initialize(&admin, &FEE_BPS);
    dc.set_tier(&Symbol::new(&env, "res"), &RESIDENTIAL);
    dc.set_tier(&Symbol::new(&env, "dc"), &DATACENTER);
    dc.set_asset_price(&token, &XLM_PRICE);

    // Fund the client with 100 XLM and approve a generous allowance.
    let tc = token::Client::new(&env, &token);
    token::StellarAssetClient::new(&env, &token).mint(&client_user, &1_000_000_000_i128);
    // Allow the settlement contract to pull up to 50 XLM until ledger 100k.
    tc.approve(&client_user, &contract, &500_000_000_i128, &100_000u32);

    TestCtx {
        env,
        contract,
        token,
        admin,
        client: client_user,
        node,
    }
}

fn nonce(env: &Env, n: u8) -> BytesN<32> {
    let mut arr = [0u8; 32];
    arr[0] = n;
    BytesN::from_array(env, &arr)
}

#[test]
fn happy_path_settles_80_20_split() {
    let ctx = setup();
    let dc = DMeshSettlementClient::new(&ctx.env, &ctx.contract);
    let tc = token::Client::new(&ctx.env, &ctx.token);

    let sid = dc.open_session(
        &ctx.client,
        &ctx.node,
        &ctx.token,
        &Symbol::new(&ctx.env, "res"),
        &nonce(&ctx.env, 1),
        &3600,
    );
    assert_eq!(sid, 1);

    // 1 GB total: residential $0.15 at XLM=$0.10 -> 1.5 XLM = 15_000_000 stroops.
    let up: u64 = 600_000_000;
    let down: u64 = 400_000_000;
    dc.propose_settlement(&sid, &up, &down);
    dc.confirm_settlement(&sid, &up, &down);

    // Node gets 80% = 1.2 XLM, protocol 20% = 0.3 XLM, client pays 1.5 XLM.
    assert_eq!(tc.balance(&ctx.node), 12_000_000);
    assert_eq!(tc.balance(&ctx.admin), 3_000_000);
    assert_eq!(tc.balance(&ctx.client), 1_000_000_000 - 15_000_000);

    let session = dc.get_session(&sid).unwrap();
    assert!(session.settled);
}

#[test]
fn datacenter_tier_uses_cheaper_rate() {
    let ctx = setup();
    let dc = DMeshSettlementClient::new(&ctx.env, &ctx.contract);
    let sid = dc.open_session(
        &ctx.client,
        &ctx.node,
        &ctx.token,
        &Symbol::new(&ctx.env, "dc"),
        &nonce(&ctx.env, 2),
        &3600,
    );
    // 1 GB at $0.03, XLM=$0.10 -> 0.3 XLM = 3_000_000 stroops.
    dc.propose_settlement(&sid, &(GB as u64), &0);
    dc.confirm_settlement(&sid, &(GB as u64), &0);
    let tc = token::Client::new(&ctx.env, &ctx.token);
    assert_eq!(tc.balance(&ctx.node), 2_400_000); // 80% of 0.3
    assert_eq!(tc.balance(&ctx.admin), 600_000); // 20% of 0.3
}

#[test]
fn node_cannot_confirm_mismatched_bytes() {
    let ctx = setup();
    let dc = DMeshSettlementClient::new(&ctx.env, &ctx.contract);
    let sid = dc.open_session(
        &ctx.client,
        &ctx.node,
        &ctx.token,
        &Symbol::new(&ctx.env, "res"),
        &nonce(&ctx.env, 3),
        &3600,
    );
    dc.propose_settlement(&sid, &500_000_000, &500_000_000);
    // Node tries to claim more traffic than the client proposed.
    let res = dc.try_confirm_settlement(&sid, &900_000_000, &900_000_000);
    assert!(res.is_err());

    // Nothing was charged: node and admin balances stay zero.
    let tc = token::Client::new(&ctx.env, &ctx.token);
    assert_eq!(tc.balance(&ctx.node), 0);
    assert_eq!(tc.balance(&ctx.admin), 0);
}

#[test]
fn replay_settlement_is_rejected() {
    let ctx = setup();
    let dc = DMeshSettlementClient::new(&ctx.env, &ctx.contract);
    let sid = dc.open_session(
        &ctx.client,
        &ctx.node,
        &ctx.token,
        &Symbol::new(&ctx.env, "res"),
        &nonce(&ctx.env, 4),
        &3600,
    );
    // 100 MB each way => 200 MB total, a non-dust charge.
    let mb: u64 = 100_000_000;
    dc.propose_settlement(&sid, &mb, &mb);
    dc.confirm_settlement(&sid, &mb, &mb);
    let res = dc.try_confirm_settlement(&sid, &mb, &mb);
    assert!(res.is_err());
}

#[test]
fn expired_session_cannot_be_settled() {
    let ctx = setup();
    let dc = DMeshSettlementClient::new(&ctx.env, &ctx.contract);
    let sid = dc.open_session(
        &ctx.client,
        &ctx.node,
        &ctx.token,
        &Symbol::new(&ctx.env, "res"),
        &nonce(&ctx.env, 5),
        &60,
    );
    // Propose while alive, then advance the clock past the TTL.
    let mb: u64 = 100_000_000;
    dc.propose_settlement(&sid, &mb, &mb);
    let future_ts = ctx.env.ledger().timestamp() + 61;
    ctx.env.ledger().set_timestamp(future_ts);
    let res = dc.try_confirm_settlement(&sid, &100_000_000, &100_000_000);
    assert!(res.is_err());

    // Expired sessions can be cancelled without any token movement.
    dc.cancel_session(&sid, &true);
}

#[test]
fn cancelled_session_never_charges() {
    let ctx = setup();
    let dc = DMeshSettlementClient::new(&ctx.env, &ctx.contract);
    let sid = dc.open_session(
        &ctx.client,
        &ctx.node,
        &ctx.token,
        &Symbol::new(&ctx.env, "res"),
        &nonce(&ctx.env, 6),
        &3600,
    );
    dc.cancel_session(&sid, &false);
    assert!(dc.try_propose_settlement(&sid, &1, &1).is_err());
    assert!(dc.try_confirm_settlement(&sid, &1, &1).is_err());
}

#[test]
fn non_admin_cannot_change_pricing() {
    let ctx = setup();
    let dc = DMeshSettlementClient::new(&ctx.env, &ctx.contract);
    // Scope mocked authorization to the non-admin node only.
    ctx.env.mock_auths(&[MockAuth {
        address: &ctx.node,
        invoke: &MockAuthInvoke {
            contract: &ctx.contract,
            fn_name: "set_tier",
            args: (
                Symbol::new(&ctx.env, "res"),
                999_999_i128,
            )
                .into_val(&ctx.env),
            sub_invokes: &[],
        },
    }]);
    let res = dc.try_set_tier(&Symbol::new(&ctx.env, "res"), &999_999);
    assert!(res.is_err());
}

#[test]
fn unknown_tier_and_asset_rejected() {
    let env = Env::default();
    env.mock_all_auths();
    let admin = Address::generate(&env);
    let user = Address::generate(&env);
    let node = Address::generate(&env);
    let token_reg = env.register_stellar_asset_contract_v2(admin.clone());
    let contract = env.register(DMeshSettlement, ());
    let dc = DMeshSettlementClient::new(&env, &contract);
    dc.initialize(&admin, &FEE_BPS);
    dc.set_asset_price(&token_reg.address(), &XLM_PRICE);
    // Tier "res" was never set.
    let res = dc.try_open_session(
        &user,
        &node,
        &token_reg.address(),
        &Symbol::new(&env, "res"),
        &nonce(&env, 7),
        &3600,
    );
    assert!(res.is_err());
}

#[test]
fn pricing_math_is_exact() {
    // 1 GB residential at $0.15 with XLM at $0.10 => 1.5 XLM in stroops.
    assert_eq!(compute_cost(GB, RESIDENTIAL, XLM_PRICE), 15_000_000);
    // 10 GB datacenter at $0.03 => $0.30 => 3 XLM.
    assert_eq!(compute_cost(10 * GB, DATACENTER, XLM_PRICE), 30_000_000);
    // Half GB residential => $0.075 => 0.75 XLM.
    assert_eq!(compute_cost(GB / 2, RESIDENTIAL, XLM_PRICE), 7_500_000);
}
