import { type FormEvent, useEffect, useRef, useState } from "react";
import { Mail, Search, UserPlus } from "lucide-react";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Badge } from "@/components/ui/badge";
import { Avatar } from "@/components/ui/avatar";
import { Select } from "@/components/ui/select";
import { useMemberStore } from "@/stores/member";
import type { UserSearchResult, WorkspaceRole } from "@/types";

interface InviteMemberDialogProps {
  open: boolean;
  onClose: () => void;
  workspaceId: string;
}

const roleOptions: { value: WorkspaceRole; label: string }[] = [
  { value: "member", label: "Member" },
  { value: "admin", label: "Admin" },
  { value: "viewer", label: "Viewer" },
];

export function InviteMemberDialog({
  open,
  onClose,
  workspaceId,
}: InviteMemberDialogProps) {
  const { addWorkspaceMember, searchUsers, userSearchResults, isSearching, clearSearchResults } =
    useMemberStore();

  const [email, setEmail] = useState("");
  const [role, setRole] = useState<WorkspaceRole>("member");
  const [selectedUser, setSelectedUser] = useState<UserSearchResult | null>(null);
  const [showDropdown, setShowDropdown] = useState(false);
  const [isSubmitting, setIsSubmitting] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const debounceRef = useRef<ReturnType<typeof setTimeout> | null>(null);
  const dropdownRef = useRef<HTMLDivElement>(null);

  // Reset form on open/close
  useEffect(() => {
    if (open) {
      setEmail("");
      setRole("member");
      setSelectedUser(null);
      setShowDropdown(false);
      setError(null);
      clearSearchResults();
    }
  }, [open, clearSearchResults]);

  // Debounced search
  useEffect(() => {
    if (debounceRef.current) {
      clearTimeout(debounceRef.current);
    }
    if (!email.trim() || selectedUser) {
      clearSearchResults();
      setShowDropdown(false);
      return;
    }
    debounceRef.current = setTimeout(() => {
      void searchUsers(workspaceId, email.trim());
      setShowDropdown(true);
    }, 300);
    return () => {
      if (debounceRef.current) {
        clearTimeout(debounceRef.current);
      }
    };
  }, [email, selectedUser, workspaceId, searchUsers, clearSearchResults]);

  const handleSelectUser = (user: UserSearchResult) => {
    setSelectedUser(user);
    setEmail(user.email);
    setShowDropdown(false);
    clearSearchResults();
  };

  const handleEmailChange = (value: string) => {
    setEmail(value);
    if (selectedUser) {
      setSelectedUser(null);
    }
  };

  const handleSubmit = async (e: FormEvent) => {
    e.preventDefault();

    const emailValue = selectedUser?.email ?? email.trim();
    if (!emailValue) {
      setError("Email is required");
      return;
    }

    setIsSubmitting(true);
    setError(null);

    try {
      await addWorkspaceMember(workspaceId, emailValue, role);
      onClose();
    } catch (err) {
      setError(err instanceof Error ? err.message : "Failed to invite member");
    } finally {
      setIsSubmitting(false);
    }
  };

  const handleOpenChange = (nextOpen: boolean) => {
    if (!nextOpen) {
      onClose();
    }
  };

  return (
    <Dialog open={open} onOpenChange={handleOpenChange}>
      <DialogContent onClose={onClose}>
        <DialogHeader>
          <DialogTitle>Invite Member</DialogTitle>
          <DialogDescription>
            Invite a user to this workspace by email address.
          </DialogDescription>
        </DialogHeader>

        <form onSubmit={handleSubmit} className="mt-4 space-y-4">
          {/* Email search */}
          <div className="space-y-1.5">
            <label htmlFor="imd-email" className="text-sm font-medium">
              Email address <span className="text-destructive">*</span>
            </label>
            <div className="relative">
              <div className="pointer-events-none absolute inset-y-0 left-3 flex items-center">
                {isSearching ? (
                  <Search className="h-4 w-4 animate-pulse text-muted-foreground" />
                ) : (
                  <Mail className="h-4 w-4 text-muted-foreground" />
                )}
              </div>
              <Input
                id="imd-email"
                type="email"
                placeholder="user@example.com"
                value={email}
                onChange={(e) => handleEmailChange(e.target.value)}
                className="pl-9"
                autoFocus
                autoComplete="off"
              />

              {/* Search results dropdown */}
              {showDropdown && userSearchResults.length > 0 && (
                <div
                  ref={dropdownRef}
                  className="absolute z-50 mt-1 w-full rounded-lg border border-border bg-card shadow-lg"
                >
                  {userSearchResults.map((user) => (
                    <button
                      key={user.id}
                      type="button"
                      className="flex w-full items-center gap-3 px-3 py-2.5 text-left hover:bg-accent disabled:cursor-not-allowed disabled:opacity-50 first:rounded-t-lg last:rounded-b-lg"
                      onClick={() => !user.is_member && handleSelectUser(user)}
                      disabled={user.is_member}
                    >
                      <Avatar
                        src={user.avatar_url || undefined}
                        name={user.name || user.email}
                        size="sm"
                      />
                      <div className="flex-1 min-w-0">
                        <p className="truncate text-sm font-medium">{user.name}</p>
                        <p className="truncate text-xs text-muted-foreground">{user.email}</p>
                      </div>
                      {user.is_member && (
                        <Badge variant="secondary" className="text-xs shrink-0">
                          Already member
                        </Badge>
                      )}
                    </button>
                  ))}
                </div>
              )}
            </div>
          </div>

          {/* Selected user preview */}
          {selectedUser && (
            <div className="flex items-center gap-3 rounded-lg border border-border px-3 py-2.5">
              <Avatar
                src={selectedUser.avatar_url || undefined}
                name={selectedUser.name || selectedUser.email}
                size="sm"
              />
              <div className="flex-1 min-w-0">
                <p className="truncate text-sm font-medium">{selectedUser.name}</p>
                <p className="truncate text-xs text-muted-foreground">{selectedUser.email}</p>
              </div>
              <button
                type="button"
                className="text-xs text-muted-foreground hover:text-foreground"
                onClick={() => {
                  setSelectedUser(null);
                  setEmail("");
                }}
              >
                Clear
              </button>
            </div>
          )}

          {/* Role selector */}
          <div className="space-y-1.5">
            <label htmlFor="imd-role" className="text-sm font-medium">
              Role
            </label>
            <Select
              id="imd-role"
              value={role}
              onChange={(e) => setRole(e.target.value as WorkspaceRole)}
            >
              {roleOptions.map((opt) => (
                <option key={opt.value} value={opt.value}>
                  {opt.label}
                </option>
              ))}
            </Select>
            <p className="text-xs text-muted-foreground">
              Members can view and edit tasks. Admins can manage settings. Viewers have read-only access.
            </p>
          </div>

          {error && <p className="text-sm text-destructive">{error}</p>}

          <DialogFooter>
            <Button
              type="button"
              variant="outline"
              onClick={onClose}
              disabled={isSubmitting}
            >
              Cancel
            </Button>
            <Button type="submit" disabled={isSubmitting}>
              {isSubmitting ? (
                "Inviting..."
              ) : (
                <>
                  <UserPlus className="h-4 w-4" />
                  Invite Member
                </>
              )}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
}
