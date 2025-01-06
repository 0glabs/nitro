<br />
<p align="center">
  <p align="center" href="https://arbitrum.io/">
  <img src="https://arbitrum.io/assets/arbitrum/logo_color.png" alt="Logo" width="120" height="120">
  </p>
  <h3 align="center">+</h3>
  <p align="center" href="https://arbitrum.io/">
  <img src="https://framerusercontent.com/images/JJi9BT4FAjp4W63c3jjNz0eezQ.png" alt="Logo" width="140" height="140">
  </p>
    <br />
  </p>
</p>


## Arbitrum 0G Integration Guide

### Overview

The Arbitrum 0G integration allows developers to deploy an Orbit Chain using 0G (Zero Gravity) for data availability. This integration marks the first AI-focused external integration to the Arbitrum Orbit protocol layer, offering an alternative to Arbitrum AnyTrust for high-performance data availability.

### Key Components

1. DA Provider Implementation: Implements the DataAvailabilityProvider interface for 0G DA.
2. Preimage Oracle Implementation: Supports fraud proofs by populating the preimage mapping with 0G hashes.
3. 0G Integration: Ensures data integrity and availability through 0G's consensus mechanism.

### 0G DA Provider Implementation
The core logic for posting and retrieving data is implemented in the zerogravity.go file. Key features include:
- ZgDA struct: Manages the connection to the 0G disperser client.
- Store method: Handles data storage on 0G, breaking large blobs into smaller chunks if necessary.
- Read method: Retrieves data from 0G using the provided blob parameters.

### Learn More About 0G

[0G Website](https://0g.ai/)
[0G Github](https://github.com/0glabs)

### Learn More About Arbitrum Orbit

[Arbitrum Orbit Docs](https://docs.arbitrum.io/launch-orbit-chain/orbit-gentle-introduction)
