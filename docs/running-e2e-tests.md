# Running E2E Tests (locally)

## Setup

### Repo setup

To run things locally, make sure you're on the `feature/setup-stellar` branch of `chainlink-ccv` (this is only required for that remaining portion of `NewCLDF...` method but otherwise not needed).

You'll notice that I've removed any StellarConfig definition from that repo.

Run `go mod tidy` in `chainlink-ccv`, `chainlink-stellar` (repo root), and `chainlink-stellar/tests`

### Network setup

Build the committee-verifier image with `make docker-verifier`

Run `make up`

This will up all the containers incl. that of the stellar verifier referenced above.

It will also output the `....-out.toml` file which the E2E test will use.

> Note: something that's a bit weird now is that debugging still requires looking into the logs of the docker container running the verifier.

Use `docker ps` and `docker logs -f $CONTAINER_ID` is useful to inspect the status of containers and read through their logs.

### Running E2E tests

Now you can run the E2E tests with `cd tests && go test -v -timeout 15m ./e2e/...`

> The `/tests` directory is its own Go module (`tests/go.mod`) that imports the
> production module (`github.com/smartcontractkit/chainlink-stellar`) and the e2e
> testing framework (CTF) via a local `replace`. Run all `go` commands for e2e,
> integration, and devenv code from inside `tests/` (or use the `make` / `just`
> recipes, which do this for you). The production module at the repo root stays
> free of direct CTF imports so other repos can import it without inheriting CTF.


---

## Debugging

...


---

## Important Notes

### Where are contracts deployed?

Contracts are deployed as a part of the toplogy spin up step (when `make up` is called) which invokes the chain's `DeployContractsForSelector(...)` method:

1. The registeration of various chain-specific implementations is done in [/tests/ccv/chain/register.go](../tests/ccv/chain/register.go)
2. One of the registered implementations is a `ChainImplFactory` (see [tests/ccv/chain/impl_factory.go](../tests/ccv/chain/impl_factory.go)) which provides an instance of the chain implementation (in this case, Stellar - see [tests/ccv/chain/chain.go](../tests/ccv/chain/chain.go))
3. The chain implmenetation incldues a method `DeployContractsForSelector(...)` which is responsible for deploying contracts to teh provided chain selector.


### Is it required to re-build all the images when making changes?

...