# Terms of use and acceptance

These terms govern use of the dmesh testnet software, coordinator and public
directory. They are intentionally short; the enforceable technical rules live
in the smart contract and the client software.

## Acceptance

- **Clients and agents** accept these terms and the
  [Acceptable Use Policy](docs/ACCEPTABLE_USE.md) by opening a session through
  the coordinator.
- **Node operators** accept them by registering a mesh address. Registration
  requires no KYC, but it does require an accurate, Stellar-signed geography
  and reachability claim; falsified claims are grounds for delisting.
- The network is provided on a best-effort **testnet** basis, without warranty
  of uptime, availability or fitness for a particular purpose. Testnet tokens
  and balances have no monetary value.

## Roles and responsibilities

- The coordinator only relays signed signaling messages, serves the public
  directory and tracks liveness and metered counters. It holds no wallet,
  cannot move funds and does not inspect tunneled traffic.
- Node operators provide egress capacity and are responsible for the traffic
  their IP address originates under the
  [Acceptable Use Policy](docs/ACCEPTABLE_USE.md).
- Clients and agents remain responsible for the requests they send and the
  data they retrieve, including any site terms and applicable law.

## Abuse

The enforced exit policy blocks email, file-sharing and other abuse-prone
ports at the node. Suspected abuse should be reported through the private
**Security → Report a vulnerability** channel on
[emrealt34/stellar-dvpn](https://github.com/emrealt34/stellar-dvpn) with the
node mesh address, timestamp and evidence. Confirmed abusive nodes are
delisted; on-chain settlement history remains immutable and auditable.

## Changes

Material changes to these terms or the acceptable use policy are committed to
this repository with review history. Continued operation after a change
constitutes acceptance.
