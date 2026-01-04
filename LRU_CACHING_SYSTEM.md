# Building an LRU Cache in Go

A step-by-step guide to implementing a Least Recently Used (LRU) cache using Go, based on doubly linked lists and hash maps.

---

## Table of Contents

1. [Overview & Prerequisites](#1-overview--prerequisites)
2. [Architecture & Data Structures](#2-architecture--data-structures)
3. [Queue Initialization](#3-queue-initialization)
4. [Cache Initialization](#4-cache-initialization)
5. [Core Operations](#5-core-operations)
6. [Usage Example](#6-usage-example)
7. [Complexity Analysis](#7-complexity-analysis)

---

## 1. Overview & Prerequisites

### What is an LRU Cache?

An LRU (Least Recently Used) cache stores a limited number of items and automatically removes the least recently accessed items when the cache reaches capacity. This prevents your backend from repeatedly calling your database by caching frequently accessed data for short periods.

### The Three Rules of a True LRU Cache

For a cache to be a "true" LRU cache, these three conditions must be met:

1. **Re-add existing items to the front**: If an item already exists in the cache, remove it and add it again to the beginning. This keeps recently used items accessible.

2. **Maintain order**: Items must be stored in order (not as an unordered data object). The order reflects when each item was last accessed.

3. **Deletion at tail, addition at head**: New items are added at the head (front) of the cache. When capacity is exceeded, the oldest item at the tail (end) is removed.

### Prerequisites

This guide assumes you are comfortable with:
- Go programming fundamentals
- Pointers and memory addresses
- Structs and struct methods
- Basic understanding of linked lists

### Project Setup

Create a new project directory and initialize the Go module:

```bash
mkdir cache-project
cd cache-project
go mod init github.com/yourname/cache-project
```

This creates a `go.mod` file that tracks your project dependencies (similar to `package.json` in JavaScript projects).

---

## 2. Architecture & Data Structures

The LRU cache is built from four interconnected data structures:

```
┌─────────────────────────────────────────────────────────┐
│                        CACHE                            │
│  ┌─────────────────────────────────────────────────┐   │
│  │                     QUEUE                        │   │
│  │   HEAD ←→ NODE ←→ NODE ←→ NODE ←→ TAIL          │   │
│  │         (newest)              (oldest)           │   │
│  └─────────────────────────────────────────────────┘   │
│  ┌─────────────────────────────────────────────────┐   │
│  │                     HASH                         │   │
│  │   "cat" → *Node    "dog" → *Node                │   │
│  │   "tree" → *Node   "blue" → *Node               │   │
│  └─────────────────────────────────────────────────┘   │
└─────────────────────────────────────────────────────────┘
```

### 2.1 Node Struct

Each node in the queue contains a value and pointers to its neighboring nodes:

```go
type Node struct {
    Value string
    Left  *Node
    Right *Node
}
```

- **Value**: The string data stored in this node
- **Left**: Pointer to the node on the left (toward head)
- **Right**: Pointer to the node on the right (toward tail)

```
         ┌───────────┐
         │   NODE    │
         ├───────────┤
         │  Value    │  ← The actual data (string)
         │  Left  *──┼──→ Points to left neighbor
         │  Right *──┼──→ Points to right neighbor
         └───────────┘
```

### 2.2 Queue Struct

The queue maintains pointers to the head and tail sentinel nodes, plus the current length:

```go
type Q struct {
    Head   *Node
    Tail   *Node
    Length int
}
```

- **Head**: Empty sentinel node; its `Right` pointer points to the first real element
- **Tail**: Empty sentinel node; its `Left` pointer points to the last real element
- **Length**: Number of actual items in the queue (not counting head/tail sentinels)

### 2.3 Hash Type

The hash provides O(1) lookups to check if a value exists and to retrieve its node:

```go
type Hash map[string]*Node
```

- **Key**: The string value stored in the cache
- **Value**: Pointer to the corresponding node in the queue

This allows instant access to any node without traversing the linked list.

### 2.4 Cache Struct

The cache combines the queue and hash together:

```go
type Cache struct {
    Q    Q
    Hash Hash
}
```

### 2.5 Cache Size Constant

Define the maximum number of items the cache can hold:

```go
const SIZE = 5
```

When the queue length exceeds this size, the oldest item (at the tail) is evicted.

---

## 3. Queue Initialization

### Understanding an Empty Queue

When the queue is empty, the head and tail sentinel nodes point to each other:

```
    ┌──────┐         ┌──────┐
    │ HEAD │ ──────→ │ TAIL │
    │      │ ←────── │      │
    └──────┘         └──────┘
      Right            Left
      points           points
      to Tail          to Head
```

When head's `Right` points to tail, and tail's `Left` points to head, there are no elements between them—the queue is empty.

### NewQueue Function

The `NewQueue` function creates and returns an initialized empty queue:

```go
func NewQueue() Q {
    head := &Node{}
    tail := &Node{}

    head.Right = tail
    tail.Left = head

    return Q{
        Head: head,
        Tail: tail,
    }
}
```

**Step-by-step breakdown:**

1. Create an empty head node: `head := &Node{}`
2. Create an empty tail node: `tail := &Node{}`
3. Point head's Right to tail: `head.Right = tail`
4. Point tail's Left to head: `tail.Left = head`
5. Return the Queue struct with Head and Tail set

**Key insight**: The head and tail are sentinel nodes—they never hold actual data. They simply mark the boundaries of the queue:
- `head.Right` always points to the **first** real element (or tail if empty)
- `tail.Left` always points to the **last** real element (or head if empty)

---

## 4. Cache Initialization

### NewCache Function

The `NewCache` function creates a cache by combining a new queue with an empty hash map:

```go
func NewCache() Cache {
    return Cache{
        Q:    NewQueue(),
        Hash: Hash{},
    }
}
```

**Step-by-step breakdown:**

1. Call `NewQueue()` to create an initialized empty queue
2. Create an empty `Hash{}` map
3. Return a Cache struct containing both

**Usage in main:**

```go
func main() {
    fmt.Println("START CACHE")
    cache := NewCache()
    // cache is now ready to use
}
```

---

## 5. Core Operations

### 5.1 Check - Main Entry Point

The `Check` method is the primary interface for adding values to the cache. It handles the LRU logic:

```go
func (c *Cache) Check(str string) {
    node := &Node{}

    if val, ok := c.Hash[str]; ok {
        node = c.Remove(val)
    } else {
        node.Value = str
    }

    c.Add(node)
    c.Hash[str] = node
}
```

**Step-by-step breakdown:**

1. Create an empty node: `node := &Node{}`
2. Check if `str` already exists in the hash map
3. **If it exists**: Remove the existing node from its current position (to re-add at front)
4. **If it doesn't exist**: Set the node's value to the string
5. Add the node to the front of the queue
6. Update the hash map to point to this node

**Why remove and re-add?** This is the LRU behavior—when an item is accessed, it becomes the "most recently used" and moves to the front.

---

### 5.2 Remove - Unlink a Node

The `Remove` method removes a node from anywhere in the queue by updating pointers:

```go
func (c *Cache) Remove(n *Node) *Node {
    fmt.Printf("REMOVE: %s\n", n.Value)

    left := n.Left
    right := n.Right

    left.Right = right
    right.Left = left

    c.Q.Length--
    delete(c.Hash, n.Value)

    return n
}
```

**Visual explanation of pointer manipulation:**

```
BEFORE removal of node 2:
    ┌───┐     ┌───┐     ┌───┐
    │ 1 │ ←→  │ 2 │ ←→  │ 3 │
    └───┘     └───┘     └───┘

AFTER removal:
    ┌───┐               ┌───┐
    │ 1 │ ←───────────→ │ 3 │
    └───┘               └───┘
         (2 is orphaned)
```

**Step-by-step breakdown:**

1. Store references to the left and right neighbors: `left := n.Left`, `right := n.Right`
2. Point the left neighbor's Right to the right neighbor: `left.Right = right`
3. Point the right neighbor's Left to the left neighbor: `right.Left = left`
4. Decrement the queue length
5. Remove the entry from the hash map
6. Return the removed node (so it can be re-added at the front)

**Key insight**: By making node 1's Right point to node 3, and node 3's Left point to node 1, node 2 becomes orphaned—nothing references it anymore, so it's effectively deleted.

---

### 5.3 Add - Insert at Head

The `Add` method inserts a node at the front of the queue (right after the head sentinel):

```go
func (c *Cache) Add(n *Node) {
    fmt.Printf("ADD: %s\n", n.Value)

    temp := c.Q.Head.Right

    c.Q.Head.Right = n
    n.Left = c.Q.Head
    n.Right = temp
    temp.Left = n

    c.Q.Length++

    if c.Q.Length > SIZE {
        c.Remove(c.Q.Tail.Left)
    }
}
```

**Visual explanation:**

```
BEFORE adding new node N:
    HEAD → [A] → [B] → TAIL

AFTER adding N:
    HEAD → [N] → [A] → [B] → TAIL
```

**Step-by-step breakdown:**

1. Store the current first element: `temp := c.Q.Head.Right`
2. Point head's Right to the new node: `c.Q.Head.Right = n`
3. Point the new node's Left to head: `n.Left = c.Q.Head`
4. Point the new node's Right to the old first element: `n.Right = temp`
5. Point the old first element's Left to the new node: `temp.Left = n`
6. Increment the queue length
7. **If over capacity**: Remove the last element (the one at the tail)

**Eviction logic**: When `Length > SIZE`, the least recently used item (at `Tail.Left`) is automatically removed.

---

### 5.4 Display - Print Queue Contents

The `Display` method prints all values in the queue for debugging:

**Cache Display method (calls Queue Display):**

```go
func (c *Cache) Display() {
    c.Q.Display()
}
```

**Queue Display method:**

```go
func (q *Q) Display() {
    node := q.Head.Right
    fmt.Printf("%d - [", q.Length)

    for i := 0; i < q.Length; i++ {
        fmt.Printf("{%s}", node.Value)
        if i < q.Length-1 {
            fmt.Printf(" <-> ")
        }
        node = node.Right
    }

    fmt.Println("]")
}
```

**Step-by-step breakdown:**

1. Start at the first real element: `node := q.Head.Right`
2. Print the queue length
3. Loop through all elements
4. For each node, print its value in curly braces
5. Print arrows between elements (except after the last one)
6. Move to the next node: `node = node.Right`

**Example output:**
```
5 - [{dog} <-> {tree} <-> {tomato} <-> {potato} <-> {dragonfruit}]
```

---

## 6. Usage Example

### Main Function

Here's how to use the cache in your main function:

```go
func main() {
    fmt.Println("START CACHE")
    cache := NewCache()

    for _, word := range []string{
        "parrot",
        "avocado",
        "dragonfruit",
        "tree",
        "potato",
        "tomato",
        "tree",
        "dog",
    } {
        cache.Check(word)
        cache.Display()
    }
}
```

### Expected Output Walkthrough

Running `go run main.go` produces the following output:

```
START CACHE
ADD: parrot
1 - [{parrot}]
ADD: avocado
2 - [{avocado} <-> {parrot}]
ADD: dragonfruit
3 - [{dragonfruit} <-> {avocado} <-> {parrot}]
ADD: tree
4 - [{tree} <-> {dragonfruit} <-> {avocado} <-> {parrot}]
ADD: potato
5 - [{potato} <-> {tree} <-> {dragonfruit} <-> {avocado} <-> {parrot}]
ADD: tomato
REMOVE: parrot
5 - [{tomato} <-> {potato} <-> {tree} <-> {dragonfruit} <-> {avocado}]
REMOVE: tree
ADD: tree
5 - [{tree} <-> {tomato} <-> {potato} <-> {dragonfruit} <-> {avocado}]
ADD: dog
REMOVE: avocado
5 - [{dog} <-> {tree} <-> {tomato} <-> {potato} <-> {dragonfruit}]
```

### Output Explanation

| Step | Action | Explanation |
|------|--------|-------------|
| 1-5 | ADD parrot through potato | Cache fills up to SIZE (5) |
| 6 | ADD tomato, REMOVE parrot | Cache exceeds SIZE, evicts oldest (parrot) |
| 7 | REMOVE tree, ADD tree | "tree" already exists, so remove from middle and re-add at front |
| 8 | ADD dog, REMOVE avocado | Cache at capacity, evicts oldest (avocado) |

**Key observations:**
- When "tree" is accessed again, it moves from position 3 to position 1 (front)
- When capacity is exceeded, the tail item (oldest) is always removed
- The hash map enables O(1) lookup to check if "tree" already exists

---

## 7. Complexity Analysis

### Time Complexity

| Operation | Complexity | Reason |
|-----------|------------|--------|
| Check (lookup) | O(1) | Hash map lookup is constant time |
| Add | O(1) | Only pointer updates, no traversal |
| Remove | O(1) | Direct access via hash, only pointer updates |
| Display | O(n) | Must traverse all n elements |

**Why O(1) for all core operations?**

The combination of a **doubly linked list** and a **hash map** enables constant-time operations:

1. **Hash map**: Provides O(1) lookup to find if a key exists and get its node pointer
2. **Doubly linked list**: Allows O(1) insertion and deletion at any position (given a pointer to the node)

Without the hash map, finding a node would require O(n) traversal. Without the doubly linked list, removal would require O(n) to find the predecessor.

### Space Complexity

| Component | Space |
|-----------|-------|
| Queue (n nodes) | O(n) |
| Hash map (n entries) | O(n) |
| **Total** | **O(n)** |

Where `n` is the maximum cache size (SIZE constant).

### Why Linked List + Hash Map?

```
┌─────────────────────────────────────────────────────────┐
│           DATA STRUCTURE COMPARISON                      │
├─────────────────────────────────────────────────────────┤
│ Structure        │ Lookup │ Insert │ Delete │ Ordered  │
├──────────────────┼────────┼────────┼────────┼──────────┤
│ Array            │ O(n)   │ O(n)   │ O(n)   │ Yes      │
│ Hash Map only    │ O(1)   │ O(1)   │ O(1)   │ No       │
│ Linked List only │ O(n)   │ O(1)   │ O(1)*  │ Yes      │
│ LL + Hash Map    │ O(1)   │ O(1)   │ O(1)   │ Yes      │
└─────────────────────────────────────────────────────────┘
* O(1) only if you have a pointer to the node
```

The **linked list + hash map** combination gives us:
- O(1) lookup (from hash map)
- O(1) insertion at head (from linked list)
- O(1) deletion anywhere (pointer from hash map + linked list removal)
- Maintained order (from linked list structure)

This is the classic solution for implementing an LRU cache efficiently.

---

## Summary

You have built an LRU cache with:

1. **Four data structures**: Node, Queue, Hash, Cache
2. **Sentinel nodes**: Head and Tail mark queue boundaries
3. **O(1) operations**: Hash map + doubly linked list combination
4. **Automatic eviction**: Oldest items removed when capacity exceeded
5. **LRU behavior**: Accessed items move to the front

This implementation demonstrates fundamental data structure concepts used in production caching systems like Redis and Memcached.
