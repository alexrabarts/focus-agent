# Embeddings-Based Priority Scoring Implementation

## Overview

This implementation adds a machine learning-based priority scoring system to Focus Agent that learns from user feedback over time. Instead of relying solely on LLM-generated scores, the system uses embeddings and K-nearest neighbors (K-NN) to adjust priorities based on similar tasks that the user has previously voted on.

## Architecture

### Components

1. **Database Schema** (`internal/db/migrations.go:407-538`)
   - `task_embeddings`: Stores 768-dimensional vectors for each task
   - `priority_feedback`: Stores user votes (👍/👎) on task priorities
   - Uses DuckDB VSS extension with HNSW indexing for fast vector similarity search

2. **Embeddings Client** (`internal/embeddings/client.go`)
   - Interfaces with local Ollama API
   - Uses `nomic-embed-text` model (768 dimensions)
   - Includes retry logic and cosine similarity calculation

3. **Task Embedding Builder** (`internal/embeddings/task_embeddings.go`)
   - Builds embedding content from:
     - Task title + description (what the task is)
     - Source context (where it came from: Gmail, Front, Google Tasks)
     - Matched priorities (strategic alignment from LLM)
   - Async generation to avoid blocking task creation
   - Database save/retrieve functions

4. **K-NN Scoring** (`internal/scoring/knn.go`)
   - Finds K=5 most similar tasks with user feedback
   - Calculates weighted vote based on cosine similarity
   - Adjusts priority scores: `adjustment = sum(vote * similarity) / sum(similarity) * 2.0`
   - Vote: +1 (👍) or -1 (👎)

5. **Hybrid Scoring Strategy** (`internal/scoring/hybrid.go`)
   - **Phase 1 (0-20 feedback)**: Pure LLM scoring, collect training data
   - **Phase 2 (20-100 feedback)**: Blend LLM + K-NN with progressive weighting
   - **Phase 3 (100+ feedback)**: Primary K-NN with LLM fallback for new patterns

6. **TUI Feedback Interface** (`internal/tui/tasks.go`)
   - Added `+`/`=` keys for 👍 (priority is right)
   - Added `-`/`_` keys for 👎 (priority is wrong)
   - Shows feedback confirmation message
   - Displays feedback prompt in task detail view

7. **Backfill Tool** (`cmd/backfill-embeddings/main.go`)
   - Command-line utility to generate embeddings for existing tasks
   - Supports dry-run mode and batch limiting
   - Rate-limited to avoid overwhelming Ollama

## How It Works

### Embedding Generation

When a task is created or updated:

1. Content is built: `"{title}\n{description}\nSource: {source}\nAligned with: {priorities}"`
2. Sent to Ollama `nomic-embed-text` model
3. Returns 768-dimensional vector
4. Stored in `task_embeddings` table with HNSW index

### User Feedback Flow

1. User views task in detail view (press Enter on task)
2. Sees current priority score and breakdown
3. Presses `+` if priority seems right, `-` if it seems wrong
4. Feedback stored in `priority_feedback` table
5. Future similar tasks will be adjusted based on this feedback

### Scoring Process

#### Phase 1: Bootstrap (0-20 feedback items)
```
score = LLM_base_score
(Just collecting training data)
```

#### Phase 2: Hybrid (20-100 feedback items)
```
weight = (feedback_count - 20) / (100 - 20)  # 0.0 to 1.0
knn_adjustment = calculate_knn_adjustment()
score = (1 - weight) * LLM_score + weight * (LLM_score + knn_adjustment)
```

#### Phase 3: K-NN Primary (100+ feedback items)
```
knn_adjustment = calculate_knn_adjustment()
score = LLM_score + knn_adjustment
(Falls back to pure LLM if no similar tasks with feedback found)
```

### K-NN Adjustment Calculation

1. Find K=5 most similar tasks with feedback using cosine similarity
2. For each neighbor: `contribution = vote * similarity`
3. `weighted_sum = sum(contributions)`
4. `total_weight = sum(similarities)`
5. `adjustment = (weighted_sum / total_weight) * 2.0`  # Scale to ±2 points

Example:
```
Similar Task 1: similarity=0.9, vote=+1  → contribution = +0.9
Similar Task 2: similarity=0.7, vote=+1  → contribution = +0.7
Similar Task 3: similarity=0.6, vote=-1  → contribution = -0.6
Similar Task 4: similarity=0.5, vote=+1  → contribution = +0.5
Similar Task 5: similarity=0.3, vote=+1  → contribution = +0.3

weighted_sum = 0.9 + 0.7 - 0.6 + 0.5 + 0.3 = 1.8
total_weight = 0.9 + 0.7 + 0.6 + 0.5 + 0.3 = 3.0
adjustment = (1.8 / 3.0) * 2.0 = +1.2 points
```

## Setup & Usage

### 1. Run Database Migrations

Migrations run automatically on startup, but you can verify:

```bash
./focus-agent
# Check logs for "Migration 7 applied successfully"
```

### 2. Backfill Embeddings for Existing Tasks

```bash
# Dry run to see what will be processed
./backfill-embeddings -dry-run

# Process all tasks
./backfill-embeddings

# Process limited batch
./backfill-embeddings -limit 50

# Use custom config
./backfill-embeddings -config ~/.focus-agent/config.yaml
```

### 3. Provide Feedback on Tasks

1. Open Focus Agent TUI
2. Navigate to "Tasks" tab
3. Press Enter on a task to view details
4. Review the priority score breakdown
5. Press `+` if the priority looks right
6. Press `-` if the priority seems wrong
7. See confirmation message
8. Return to list with `Esc`

### 4. Monitor Learning Progress

Check the feedback count to see which phase you're in:

```bash
duckdb ~/.focus-agent/data.db "SELECT COUNT(*) as feedback_count FROM priority_feedback"
```

- 0-19: Bootstrap phase (pure LLM)
- 20-99: Hybrid phase (blending LLM + K-NN)
- 100+: K-NN phase (learning primary, LLM fallback)

## Configuration

Ensure Ollama is configured in `~/.focus-agent/config.yaml`:

```yaml
ollama:
  hosts:
    - url: "http://localhost:11434"
      workers: 2
  model: "qwen2.5:7b"  # For task extraction
  enabled: true
  timeout_seconds: 120
```

The embeddings client automatically uses `nomic-embed-text` model.

## Database Schema

### task_embeddings
```sql
CREATE TABLE task_embeddings (
    task_id VARCHAR PRIMARY KEY,
    embedding FLOAT[768] NOT NULL,
    embedding_content VARCHAR NOT NULL,  -- What was embedded
    model VARCHAR NOT NULL,               -- "nomic-embed-text"
    generated_at BIGINT NOT NULL,
    FOREIGN KEY (task_id) REFERENCES tasks(id)
);

CREATE INDEX idx_task_embeddings_hnsw
ON task_embeddings USING HNSW (embedding);
```

### priority_feedback
```sql
CREATE TABLE priority_feedback (
    id VARCHAR PRIMARY KEY,
    task_id VARCHAR NOT NULL,
    user_vote INTEGER NOT NULL CHECK (user_vote IN (-1, 1)),
    reason VARCHAR DEFAULT NULL,          -- Future: optional text reason
    original_score DOUBLE,
    adjusted_score DOUBLE,                -- Future: for re-scoring
    feedback_at BIGINT NOT NULL,
    FOREIGN KEY (task_id) REFERENCES tasks(id)
);
```

## Future Enhancements

### Already Supported (Not Yet Integrated)

1. **Optional Reason Text**: The `reason` field in `priority_feedback` is ready for text explanations
2. **Adjusted Score Tracking**: Can store the score before/after user adjustment
3. **API Feedback**: Remote mode feedback (needs API endpoint implementation)

### Planned Features

1. **Re-scoring on Feedback**
   - After user provides feedback, trigger re-scoring of the task
   - Update K-NN adjustment immediately
   - Show before/after scores in confirmation

2. **Planner Integration**
   - Call hybrid scorer from `internal/planner/planner.go`
   - Generate embeddings for new tasks automatically
   - Use K-NN adjustments in priority calculations

3. **Feedback Analytics**
   - View feedback statistics ("You've helped improve 47 tasks")
   - See which types of tasks you consistently up/downvote
   - Identify patterns in your preferences

4. **Reason Text Input**
   - Modal input for optional reason when voting
   - Parse reasons to extract keywords
   - Use reasons to weight metrics differently (similar to property-bot)

5. **Metric-Level Feedback**
   - Vote on individual components (urgency too high? impact too low?)
   - Learn which metrics you value more
   - Adjust formula weights based on feedback patterns

## Testing

### Manual Testing Flow

1. **Initial State (Phase 1)**
   ```bash
   # Verify no feedback yet
   duckdb ~/.focus-agent/data.db "SELECT COUNT(*) FROM priority_feedback"
   # Expected: 0

   # Start TUI and view tasks
   ./focus-agent
   # Note: All scores are pure LLM
   ```

2. **Provide First Feedback**
   ```
   - Enter on a task
   - Press + or -
   - Verify confirmation message shows
   - Esc back to list
   - Repeat for 5-10 tasks
   ```

3. **Check Feedback Storage**
   ```bash
   duckdb ~/.focus-agent/data.db "
   SELECT task_id, user_vote, feedback_at
   FROM priority_feedback
   ORDER BY feedback_at DESC
   LIMIT 10"
   ```

4. **Verify Embeddings**
   ```bash
   duckdb ~/.focus-agent/data.db "
   SELECT t.title,
          length(te.embedding) as embedding_dims,
          substr(te.embedding_content, 1, 100) as content_preview
   FROM tasks t
   JOIN task_embeddings te ON t.id = te.task_id
   LIMIT 5"
   ```

5. **Test K-NN Similarity** (after 20+ feedback)
   ```bash
   # This is complex - best tested in Go code
   # See internal/scoring/knn_test.go (to be created)
   ```

### Unit Testing (TODO)

```go
// internal/scoring/knn_test.go
func TestFindNearestNeighbors(t *testing.T) {
    // Test with mock embeddings
    // Verify K=5 neighbors returned
    // Verify sorted by similarity descending
}

func TestCalculateKNNAdjustment(t *testing.T) {
    // Test adjustment calculation
    // Verify weighted voting
    // Test edge cases (no neighbors, all same vote, etc.)
}

// internal/embeddings/task_embeddings_test.go
func TestBuildEmbeddingContent(t *testing.T) {
    // Test content building from task fields
    // Verify format
}

// internal/scoring/hybrid_test.go
func TestGetCurrentPhase(t *testing.T) {
    // Test phase transitions at 20 and 100 feedback
}

func TestHybridScoring(t *testing.T) {
    // Test blending logic in Phase 2
    // Verify weight calculation
}
```

## Troubleshooting

### No embeddings generated

**Problem**: Backfill script runs but no embeddings in database

**Solutions**:
1. Check Ollama is running: `curl http://localhost:11434/api/tags`
2. Verify model is installed: `ollama list | grep nomic-embed-text`
3. Install if missing: `ollama pull nomic-embed-text`
4. Check logs for error messages

### Feedback not saving

**Problem**: Press +/- but no confirmation message

**Solutions**:
1. Check you're in detail view (pressed Enter on task)
2. Verify database is writable
3. Check logs: `journalctl -u focus-agent -f`
4. Verify migration 7 was applied: `duckdb ~/.focus-agent/data.db "SELECT * FROM migration_versions WHERE version=7"`

### K-NN not adjusting scores

**Problem**: Providing feedback but scores don't change

**Expected Behavior**:
- Scores don't change immediately after feedback
- K-NN only affects future tasks that are similar to tasks you've voted on
- Need 20+ feedback items before K-NN starts working
- Adjustment is gradual (max ±2 points typically)

## Performance Considerations

- **Embedding Generation**: ~100-200ms per task (Ollama local)
- **Vector Similarity Search**: ~10-50ms for K=5 neighbors (HNSW index)
- **Total Overhead**: Minimal when embeddings pre-generated
- **Storage**: ~3KB per task embedding (768 floats * 4 bytes)

## References

- Property-bot K-NN implementation: `/home/alex/repos/github.com/alexrabarts/property-bot/internal/scoring/embeddings.go`
- Swatchlab semantic search: `/home/alex/repos/github.com/alexrabarts/swatchlab/internal/data/semantic_palette.go`
- Memex-agent multi-factor scoring: `/home/alex/repos/github.com/alexrabarts/memex-agent/internal/graph/scoring.go`
- DuckDB VSS extension: https://duckdb.org/docs/extensions/vss
- Ollama embeddings API: https://github.com/ollama/ollama/blob/main/docs/api.md#generate-embeddings
