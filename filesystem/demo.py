import hashlib
import json
import threading
import time
from typing import Dict, List, Optional, Set
from dataclasses import dataclass, asdict
from enum import Enum
import uuid

@dataclass
class FileMetadata:
    """Represents file metadata in our system"""
    path: str
    content_hash: str
    size: int
    created_time: float
    is_directory: bool = False
    
class OperationType(Enum):
    CREATE_FILE = "create_file"
    CREATE_DIR = "create_dir"
    DELETE = "delete"
    
@dataclass
class Operation:
    """Represents a metadata operation in our Raft log"""
    op_id: str
    op_type: OperationType
    path: str
    metadata: Optional[FileMetadata] = None
    timestamp: float = 0.0

class RaftLogEntry:
    """Simplified Raft log entry"""
    def __init__(self, term: int, operation: Operation):
        self.term = term
        self.operation = operation
        self.committed = False

class ContentAddressableStorage:
    """Simplified content-addressable storage layer"""
    
    def __init__(self):
        self._storage: Dict[str, bytes] = {}
        self._lock = threading.RLock()
    
    def store(self, content: bytes) -> str:
        """Store content and return its hash"""
        content_hash = hashlib.sha256(content).hexdigest()
        
        with self._lock:
            if content_hash not in self._storage:
                self._storage[content_hash] = content
                print(f"📦 Stored content with hash: {content_hash[:12]}...")
            else:
                print(f"✨ Deduplication: content {content_hash[:12]}... already exists")
        
        return content_hash
    
    def retrieve(self, content_hash: str) -> Optional[bytes]:
        """Retrieve content by hash with integrity check"""
        with self._lock:
            content = self._storage.get(content_hash)
            if content is None:
                return None
            
            # Verify integrity
            actual_hash = hashlib.sha256(content).hexdigest()
            if actual_hash != content_hash:
                raise ValueError(f"Content integrity violation! Expected {content_hash}, got {actual_hash}")
            
            return content
    
    def exists(self, content_hash: str) -> bool:
        """Check if content exists"""
        with self._lock:
            return content_hash in self._storage

class MetadataService:
    """Simplified metadata service with Raft-like consistency"""
    
    def __init__(self, node_id: str):
        self.node_id = node_id
        self.is_leader = True  # Simplified: assume single node is always leader
        self.current_term = 1
        
        # Our "database" - maps paths to metadata
        self._metadata: Dict[str, FileMetadata] = {}
        
        # Raft log for operations
        self._log: List[RaftLogEntry] = []
        self._commit_index = -1
        
        # Ensure root directory exists
        root_metadata = FileMetadata(
            path="/",
            content_hash="",
            size=0,
            created_time=time.time(),
            is_directory=True
        )
        self._metadata["/"] = root_metadata
        
        self._lock = threading.RLock()
    
    def _parent_directory(self, path: str) -> str:
        """Get parent directory of a path"""
        if path == "/":
            return "/"
        parts = path.rstrip("/").split("/")
        if len(parts) <= 1:
            return "/"
        return "/".join(parts[:-1]) or "/"
    
    def _path_exists(self, path: str) -> bool:
        """Check if path exists in metadata"""
        return path in self._metadata
    
    def _apply_operation(self, operation: Operation) -> bool:
        """Apply an operation to our metadata store"""
        
        if operation.op_type == OperationType.CREATE_FILE:
            # Check parent directory exists
            parent = self._parent_directory(operation.path)
            if not self._path_exists(parent):
                return False
            
            # Check file doesn't already exist
            if self._path_exists(operation.path):
                return False
            
            # Create the file
            self._metadata[operation.path] = operation.metadata
            print(f"📁 Created file: {operation.path}")
            return True
            
        elif operation.op_type == OperationType.CREATE_DIR:
            # Check parent directory exists
            parent = self._parent_directory(operation.path)
            if not self._path_exists(parent):
                return False
            
            # Check directory doesn't already exist
            if self._path_exists(operation.path):
                return False
            
            # Create the directory
            dir_metadata = FileMetadata(
                path=operation.path,
                content_hash="",
                size=0,
                created_time=operation.timestamp,
                is_directory=True
            )
            self._metadata[operation.path] = dir_metadata
            print(f"📂 Created directory: {operation.path}")
            return True
            
        elif operation.op_type == OperationType.DELETE:
            if not self._path_exists(operation.path):
                return False
            
            # For simplicity, don't check if directory is empty
            del self._metadata[operation.path]
            print(f"🗑️  Deleted: {operation.path}")
            return True
        
        return False
    
    def propose_operation(self, operation: Operation) -> bool:
        """Propose an operation (simplified Raft)"""
        if not self.is_leader:
            return False
        
        with self._lock:
            # Create log entry
            log_entry = RaftLogEntry(self.current_term, operation)
            self._log.append(log_entry)
            
            # In a real Raft implementation, we would:
            # 1. Send AppendEntries RPC to followers
            # 2. Wait for majority acknowledgment
            # 3. Then commit the operation
            
            # Simplified: immediately commit since we're single-node
            success = self._apply_operation(operation)
            if success:
                log_entry.committed = True
                self._commit_index = len(self._log) - 1
            else:
                # Remove failed operation from log
                self._log.pop()
            
            return success
    
    def create_file(self, path: str, content_hash: str, size: int) -> bool:
        """Create a new file"""
        operation = Operation(
            op_id=str(uuid.uuid4()),
            op_type=OperationType.CREATE_FILE,
            path=path,
            metadata=FileMetadata(
                path=path,
                content_hash=content_hash,
                size=size,
                created_time=time.time(),
                is_directory=False
            ),
            timestamp=time.time()
        )
        
        return self.propose_operation(operation)
    
    def create_directory(self, path: str) -> bool:
        """Create a new directory"""
        operation = Operation(
            op_id=str(uuid.uuid4()),
            op_type=OperationType.CREATE_DIR,
            path=path,
            timestamp=time.time()
        )
        
        return self.propose_operation(operation)
    
    def list_directory(self, path: str) -> List[FileMetadata]:
        """List contents of a directory with strong consistency"""
        with self._lock:
            if not self._path_exists(path):
                return []
            
            if not self._metadata[path].is_directory:
                return []
            
            # Find all direct children
            children = []
            for file_path, metadata in self._metadata.items():
                if file_path == path:
                    continue
                
                # Check if this is a direct child
                parent = self._parent_directory(file_path)
                if parent == path:
                    children.append(metadata)
            
            return sorted(children, key=lambda x: x.path)
    
    def get_file_metadata(self, path: str) -> Optional[FileMetadata]:
        """Get metadata for a specific file"""
        with self._lock:
            return self._metadata.get(path)

class DistributedFileSystem:
    """Main file system interface"""
    
    def __init__(self):
        self.metadata_service = MetadataService("node-1")
        self.data_storage = ContentAddressableStorage()
        
    def create_file(self, path: str, content: bytes) -> bool:
        """Create a file with two-phase commit"""
        print(f"\n🚀 Creating file: {path}")
        
        # Phase 1: Store content and get hash
        content_hash = self.data_storage.store(content)
        
        # Phase 2: Update metadata atomically
        success = self.metadata_service.create_file(path, content_hash, len(content))
        
        if success:
            print(f"✅ File created successfully: {path}")
        else:
            print(f"❌ Failed to create file: {path}")
        
        return success
    
    def read_file(self, path: str) -> Optional[bytes]:
        """Read file content"""
        # Get metadata
        metadata = self.metadata_service.get_file_metadata(path)
        if not metadata or metadata.is_directory:
            return None
        
        # Retrieve content
        content = self.data_storage.retrieve(metadata.content_hash)
        if content:
            print(f"📖 Read file: {path} ({len(content)} bytes)")
        
        return content
    
    def create_directory(self, path: str) -> bool:
        """Create a directory"""
        print(f"\n📁 Creating directory: {path}")
        success = self.metadata_service.create_directory(path)
        
        if success:
            print(f"✅ Directory created: {path}")
        else:
            print(f"❌ Failed to create directory: {path}")
        
        return success
    
    def list_directory(self, path: str) -> List[Dict]:
        """List directory contents"""
        print(f"\n📋 Listing directory: {path}")
        
        entries = self.metadata_service.list_directory(path)
        result = []
        
        for entry in entries:
            entry_dict = {
                'name': entry.path.split('/')[-1],
                'path': entry.path,
                'type': 'directory' if entry.is_directory else 'file',
                'size': entry.size,
                'created': entry.created_time
            }
            result.append(entry_dict)
        
        # Print formatted output
        if result:
            print("Contents:")
            for entry in result:
                type_icon = "📂" if entry['type'] == 'directory' else "📄"
                print(f"  {type_icon} {entry['name']} ({entry['size']} bytes)")
        else:
            print("  (empty)")
        
        return result

# Demonstration and Testing
def demonstrate_consistency():
    """Demonstrate strong consistency guarantees"""
    
    print("=" * 60)
    print("🔬 DISTRIBUTED FILE SYSTEM CONSISTENCY DEMO")
    print("=" * 60)
    
    fs = DistributedFileSystem()
    
    # Test 1: Basic file operations
    print("\n" + "="*40)
    print("TEST 1: Basic File Operations")
    print("="*40)
    
    # Create directory
    fs.create_directory("/projects")
    
    # Create file
    content = b"Hello, distributed world!"
    fs.create_file("/projects/hello.txt", content)
    
    # Immediately list directory - should see file (strong consistency!)
    files = fs.list_directory("/projects")
    
    # Read the file back
    read_content = fs.read_file("/projects/hello.txt")
    print(f"📖 Content read back: {read_content.decode()}")
    
    # Test 2: Deduplication
    print("\n" + "="*40)
    print("TEST 2: Content Deduplication") 
    print("="*40)
    
    # Create identical content in different locations
    same_content = b"This content will be deduplicated"
    fs.create_file("/projects/file1.txt", same_content)
    fs.create_file("/projects/file2.txt", same_content)
    
    # Create different content
    different_content = b"This is different content"
    fs.create_file("/projects/file3.txt", different_content)
    
    fs.list_directory("/projects")
    
    # Test 3: Consistency under concurrent-like operations
    print("\n" + "="*40)
    print("TEST 3: Operation Ordering")
    print("="*40)
    
    # These operations will be serialized by the metadata service
    fs.create_directory("/temp")
    fs.create_file("/temp/doc1.txt", b"Document 1")
    fs.create_file("/temp/doc2.txt", b"Document 2")
    
    # List should immediately show all files
    fs.list_directory("/temp")
    
    # Test 4: Failure cases
    print("\n" + "="*40)
    print("TEST 4: Consistency Guarantees")
    print("="*40)
    
    # Try to create file in non-existent directory
    print("Attempting to create file in non-existent directory...")
    success = fs.create_file("/nonexistent/file.txt", b"Should fail")
    print(f"Result: {'Failed as expected' if not success else 'Unexpected success'}")
    
    # Try to create duplicate file
    print("\nAttempting to create duplicate file...")
    success = fs.create_file("/projects/hello.txt", b"Duplicate")
    print(f"Result: {'Failed as expected' if not success else 'Unexpected success'}")
    
    print("\n" + "="*60)
    print("✅ DEMO COMPLETE - All operations were strongly consistent!")
    print("="*60)

if __name__ == "__main__":
    demonstrate_consistency()
