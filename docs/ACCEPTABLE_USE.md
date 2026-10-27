# Acceptable use and exit policy

dmesh is a non-custodial bandwidth network. Node operators and clients both
have responsibilities, and the software enforces a baseline egress policy
that no node can disable through the normal API.

## Enforced exit policy

The proxy and P2P tunnel refuse these destination ports before any connection
is made (`internal/netguard`, verified in tests):

| Category | Ports |
| --- | --- |
| Email submission / spam | 25, 465, 587 |
| Windows sharing / RPC | 135, 137, 138, 139, 445 |
| Amplification / legacy UDP services | 17, 19, 69 |
| Portmap / Sun RPC | 111 |
| Legacy remote shells | 512, 513, 514 |
| Network file system | 2049 |

They also block non-public destinations entirely: loopback, RFC1918 private
ranges, link-local and cloud metadata addresses (including
`169.254.169.254` and the IPv6 equivalents), multicast and unspecified
addresses. DNS-rebinding is closed by validating the exact IP at dial time.

## Prohibited activity

Users and node operators must not use the network to:

- send unsolicited email, bulk spam, phishing or malware;
- attack, scan or enumerate systems the operator does not own;
- distribute illegal content or infringe intellectual property;
- evade the exit policy, including by tunneling blocked services over
  permitted ports;
- operate nodes that falsify their signed geography/tier claim.

## Abuse reporting

1. Reports are handled through the repository's private security reporting
   channel (**Security → Report a vulnerability** on
   [emrealt34/stellar-dvpn](https://github.com/emrealt34/stellar-dvpn)).
2. Include the destination, timestamp, the offending node mesh address
   (`G...`) and supporting evidence.
3. Confirmed abusive nodes are delisted from the directory and, where
   applicable, their sessions are refused. On-chain settlement history
   remains auditable and cannot be rewritten.

## Operator obligations

- Keep the node's signed geo/tier claim accurate; clients independently
  re-classify the endpoint and reject mismatches.
- Publish a reachable abuse contact alongside any production deployment and
  honor takedown requests within applicable legal timeframes.
- Store no traffic logs beyond the metered byte counters required for
  settlement; the design carries no user browsing data.
