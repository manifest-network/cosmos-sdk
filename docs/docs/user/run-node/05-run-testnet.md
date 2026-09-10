---
sidebar_position: 1
---

# Running a Testnet

:::note Synopsis
The `simd testnet` subcommand makes it easy to initialize and start a simulated test network for testing purposes.
:::

In addition to the commands for [running a node](./01-run-node.md), the `simd` binary also includes a `testnet` command that allows you to start a simulated test network in-process or to initialize files for a simulated test network that runs in a separate process.

## Initialize Files

First, let's take a look at the `init-files` subcommand.

This is similar to the `init` command when initializing a single node, but in this case we are initializing multiple nodes, generating the genesis transactions for each node, and then collecting those transactions.

The `init-files` subcommand initializes the necessary files to run a test network in a separate process (i.e. using a Docker container). Running this command is not a prerequisite for the `start` subcommand ([see below](#start-testnet)).

In order to initialize the files for a test network, run the following command:

```bash
simd testnet init-files
```

You should see the following output in your terminal:

```bash
Successfully initialized 4 node directories
```

The default output directory is a relative `.testnets` directory. Let's take a look at the files created within the `.testnets` directory.

### gentxs

The `gentxs` directory includes a genesis transaction for each validator node. Each file includes a JSON encoded genesis transaction used to register a validator node at the time of genesis. The genesis transactions are added to the `genesis.json` file within each node directory during the initilization process.

### nodes

A node directory is created for each validator node. Within each node directory is a `simd` directory. The `simd` directory is the home directory for each node, which includes the configuration and data files for that node (i.e. the same files included in the default `~/.simapp` directory when running a single node).

## Start Testnet

Now, let's take a look at the `start` subcommand.

The `start` subcommand both initializes and starts an in-process test network. This is the fastest way to spin up a local test network for testing purposes.

You can start the local test network by running the following command:

```bash
simd testnet start
```

You should see something similar to the following:

```bash
acquiring test network lock
preparing test network with chain-id "chain-mtoD9v"


+++++++++++++++++++++++++++++++++++++++++++++++++++++++++++++
++       THIS MNEMONIC IS FOR TESTING PURPOSES ONLY        ++
++                DO NOT USE IN PRODUCTION                 ++
++                                                         ++
++  sustain know debris minute gate hybrid stereo custom   ++
++  divorce cross spoon machine latin vibrant term oblige  ++
++   moment beauty laundry repeat grab game bronze truly   ++
+++++++++++++++++++++++++++++++++++++++++++++++++++++++++++++


starting test network...
started test network
press the Enter Key to terminate
```

The first validator node is now running in-process, which means the test network will terminate once you either close the terminal window or you press the Enter key. In the output, the mnemonic phrase for the first validator node is provided for testing purposes. The validator node is using the same default addresses being used when initializing and starting a single node (no need to provide a `--node` flag).

Check the status of the first validator node:

```shell
simd status
```

Import the key from the provided mnemonic:

```shell
simd keys add test --recover --keyring-backend test
```

Check the balance of the account address:

```shell
simd q bank balances [address]
```

Use this test account to manually test against the test network.

## Testnet Options

You can customize the configuration of the test network with flags. In order to see all flag options, append the `--help` flag to each command.

## Create a Testnet From Existing State

Applications that register `server.AddTestnetCreatorCommand` and provide an
application-specific state conversion can expose `in-place-testnet`. This command
converts a stopped node's local state into a testnet controlled by its local
validator key and a supplied operator account. See [Application Testnets](../../build/building-apps/05-app-testnet.md)
for application wiring. The SDK's `simd` binary does not register this command by
default; use a binary whose application has implemented the conversion.

### Before Running the Command

Complete these preparation steps manually, before invoking `in-place-testnet`:

1. Stop the source node and make a disposable copy of its home directory. The
   copy must contain committed blocks and their commit records. Keep a backup of
   the copy so you can restore it if conversion fails.
2. Install a fresh local consensus key in the copied home's configured private
   validator key file (normally `config/priv_validator_key.json`). Do not reuse a
   production validator key. Prefer copying the complete key file generated by a
   freshly initialized local node. The `address`, `pub_key`, and `priv_key` fields
   must all be present and mutually consistent: the public key must match the
   private key, and the address must match the public key. The command requires
   this file to exist; it does not generate or replace the key for you.
3. Replace the copied signing-state file's contents (normally
   `data/priv_validator_state.json`) with the JSON below. Keep the file present;
   deleting it does not reset the signing state for this command. Remove any old
   signature and sign bytes by replacing the complete contents:

   ```json
   {"height":"0","round":0,"step":0}
   ```

4. Install a fresh local node key, clear `persistent_peers` and `seeds` in
   `config/config.toml`, disable peer exchange (`p2p.pex`) and state sync
   (`statesync.enable`), and isolate the fork's P2P network from the source
   network. These are operator actions; the command does not perform network
   isolation or replace the node key.
5. Choose a distinct chain ID and a local operator account. Follow the
   application's instructions for its operator account format, signing key,
   authority settings, and any custom state-conversion requirements.

### Convert and Start the Copied Node

In this example, `APP_BINARY` is the path to your application's registered binary,
`FORK_HOME` is the prepared disposable home, and `OPERATOR_ADDRESS` is its local
operator account address:

```shell
"$APP_BINARY" in-place-testnet my-fork-chain "$OPERATOR_ADDRESS" --home "$FORK_HOME"
```

The command asks for confirmation before modifying the copied state. Answer
`y` or `yes` to proceed; use `--skip-confirmation` only when that confirmation
should be omitted, such as in an automated rehearsal.

Before conversion, the command checks that the consensus key and signing-state
files exist and can be decoded, and that signing height, round, and step are
zero with no signature or sign bytes. After preflight succeeds, it removes the
configured consensus WAL and its numbered rotation files so copied consensus
messages cannot be replayed on the fork. It also replaces the copied address book
with an empty one and clears pending source-chain evidence from the configured
evidence database. Committed evidence records are preserved.

The conversion replaces the consensus validator set and reconstructs its last
commit for the new chain ID. When vote extensions are enabled at that height,
the replacement commit includes a signed empty extension; application-specific
extension handling must accept that initial payload. The genesis file and the
genesis document cached by CometBFT both use the new chain ID.

If the application has committed one block beyond CometBFT's saved state, the
conversion can recover that state from stored block responses. It does not replay
that block's events, so its transactions may be missing from the transaction
index. If complete transaction indexing is required, recover the source node
normally and stop it cleanly before making the copy.

The optional `--trigger-testnet-upgrade <handler-name>` flag is passed to the
application's conversion callback. The application must register the handler and
implement its scheduling; consult its documentation for the supported name and
execution height.

The command starts the testnet immediately. Wait for its first block to commit
before stopping it, then use the ordinary `start` command for subsequent restarts.
Run `in-place-testnet` only once on each copied home.
