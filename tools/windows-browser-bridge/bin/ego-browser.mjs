#!/usr/bin/env node
import { main } from "../src/cli.mjs";

main().then(code => { if (typeof code === "number") process.exitCode = code; }).catch(error => {
  process.stderr.write(`${error.stack || error}\n`);
  process.exitCode = 1;
});
