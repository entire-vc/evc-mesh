# Hold gate — live control

This file exists only to be merged, and then to be deleted.

It is the positive half of the pair that proves the `hold` required check
discriminates: a pull request carrying no hold label must still merge normally.
A gate that refuses everything is indistinguishable from a working one until
somebody needs to ship, and by then it has frozen the repository.

The negative half is the same pull request with `hold` applied, which must be
refused. Both halves are recorded in the task that introduced the gate.
