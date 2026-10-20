// SPDX-License-Identifier: MIT
pragma solidity ^0.8.20;

interface IERC20 { function transfer(address, uint256) external returns (bool); }

contract Child { uint256 public x = 7; }

contract Hard {
    string public label = "a dynamic string that is longer than thirty-two bytes for sure";
    mapping(address => uint256) public m;
    address public child;
    address public owner;

    modifier onlyOwner() { require(msg.sender == owner, "not owner"); _; }
    constructor() { owner = msg.sender; }

    // dynamic return type
    function getLabel() external view returns (string memory) { return label; }
    // dynamic array in memory
    function sum(uint256[] calldata xs) external pure returns (uint256 t) {
        for (uint256 i = 0; i < xs.length; i++) t += xs[i];
    }
    // CREATE
    function spawn() external onlyOwner returns (address) {
        Child c = new Child();
        child = address(c);
        return child;
    }
    // external CALL into another contract
    function forward(address token, address to, uint256 v) external returns (bool) {
        return IERC20(token).transfer(to, v);
    }
    // DELEGATECALL
    function delegate(address impl, bytes calldata data) external onlyOwner returns (bytes memory) {
        (bool ok, bytes memory out) = impl.delegatecall(data);
        require(ok, "delegate failed");
        return out;
    }
    // abi.encode / keccak over dynamic data
    function digest(string calldata s, uint256 n) external pure returns (bytes32) {
        return keccak256(abi.encode(s, n));
    }
}
