import { type FormEvent, useEffect, useRef, useState } from "react";
import { Eye, EyeOff, Mail, Search, UserPlus } from "lucide-react";
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
import { InviteLinkBox } from "@/components/invite-link-box";
import { useMemberStore } from "@/stores/member";
import type { InviteDelivery, UserSearchResult, WorkspaceRole } from "@/types";

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

type InviteMode = "email_link" | "create_now";

export function InviteMemberDialog({
  open,
  onClose,
  workspaceId,
}: InviteMemberDialogProps) {
  const {
    addWorkspaceMember,
    createInvite,
    searchUsers,
    userSearchResults,
    isSearching,
    clearSearchResults,
  } = useMemberStore();

  const [email, setEmail] = useState("");
  const [role, setRole] = useState<WorkspaceRole>("member");
  const [mode, setMode] = useState<InviteMode>("email_link");
  const [password, setPassword] = useState("");
  const [showPassword, setShowPassword] = useState(false);
  const [selectedUser, setSelectedUser] = useState<UserSearchResult | null>(null);
  const [showDropdown, setShowDropdown] = useState(false);
  const [isSubmitting, setIsSubmitting] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [successMsg, setSuccessMsg] = useState<string | null>(null);
  // Set when an invite was created but not emailed. The dialog then stays open
  // and hands the link over, because closing it would strand the invitation:
  // nothing was sent and this is the only place the link is shown.
  const [handoff, setHandoff] = useState<InviteDelivery | null>(null);
  const debounceRef = useRef<ReturnType<typeof setTimeout> | null>(null);

  useEffect(() => {
    if (open) {
      setEmail("");
      setRole("member");
      setMode("email_link");
      setPassword("");
      setShowPassword(false);
      setSelectedUser(null);
      setShowDropdown(false);
      setError(null);
      setSuccessMsg(null);
      setHandoff(null);
      clearSearchResults();
    }
  }, [open, clearSearchResults]);

  useEffect(() => {
    if (debounceRef.current) clearTimeout(debounceRef.current);
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
      if (debounceRef.current) clearTimeout(debounceRef.current);
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
    if (selectedUser) setSelectedUser(null);
  };

  const handleSubmit = async (e: FormEvent) => {
    e.preventDefault();
    const emailValue = selectedUser?.email ?? email.trim();
    if (!emailValue) {
      setError("Email is required");
      return;
    }
    if (mode === "create_now" && !selectedUser && !password) {
      setError("Password is required when creating an account now");
      return;
    }

    setIsSubmitting(true);
    setError(null);
    setSuccessMsg(null);
    setHandoff(null);

    try {
      if (mode === "email_link" && !selectedUser) {
        // Only claim the invite was emailed if it actually was. This used to be
        // an unconditional "Invite sent to …", which was wrong on every
        // instance without a mail server — the invitee got nothing and the
        // inviter had no way to find out.
        const invite = await createInvite(workspaceId, emailValue, role);
        if (invite.email_sent) {
          setSuccessMsg(`Invite sent to ${emailValue}`);
          setTimeout(onClose, 1500);
        } else {
          setHandoff(invite);
        }
      } else if (mode === "create_now" && !selectedUser) {
        await addWorkspaceMember(workspaceId, emailValue, role, password);
        onClose();
      } else {
        // Selected existing user — always direct add (no password needed).
        await addWorkspaceMember(workspaceId, emailValue, role);
        onClose();
      }
    } catch (err) {
      setError(err instanceof Error ? err.message : "Failed to add member");
    } finally {
      setIsSubmitting(false);
    }
  };

  return (
    <Dialog open={open} onOpenChange={(next) => { if (!next) onClose(); }}>
      <DialogContent onClose={onClose}>
        <DialogHeader>
          <DialogTitle>Add Member</DialogTitle>
          <DialogDescription>
            Add a user to this workspace by email address.
          </DialogDescription>
        </DialogHeader>

        <form onSubmit={handleSubmit} className="mt-4 space-y-4">
          {/* Email */}
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
              {showDropdown && userSearchResults.length > 0 && (
                <div className="absolute z-50 mt-1 w-full rounded-lg border border-border bg-card shadow-lg">
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
                onClick={() => { setSelectedUser(null); setEmail(""); }}
              >
                Clear
              </button>
            </div>
          )}

          {/* Mode toggle — only relevant when adding an unregistered email */}
          {!selectedUser && (
            <fieldset className="space-y-2">
              <legend className="text-sm font-medium">How to add</legend>
              <label className="flex cursor-pointer items-start gap-2.5">
                <input
                  type="radio"
                  name="invite-mode"
                  value="email_link"
                  checked={mode === "email_link"}
                  onChange={() => { setMode("email_link"); setPassword(""); }}
                  className="mt-0.5"
                />
                <span>
                  <span className="block text-sm font-medium">Send invite link</span>
                  <span className="block text-xs text-muted-foreground">
                    User receives an email and sets their own password.
                  </span>
                </span>
              </label>
              <label className="flex cursor-pointer items-start gap-2.5">
                <input
                  type="radio"
                  name="invite-mode"
                  value="create_now"
                  checked={mode === "create_now"}
                  onChange={() => setMode("create_now")}
                  className="mt-0.5"
                />
                <span>
                  <span className="block text-sm font-medium">Create now with password</span>
                  <span className="block text-xs text-muted-foreground">
                    Account is created immediately — share credentials manually.
                  </span>
                </span>
              </label>
            </fieldset>
          )}

          {/* Password — only for create_now with unknown user */}
          {mode === "create_now" && !selectedUser && (
            <div className="space-y-1.5">
              <label htmlFor="imd-password" className="text-sm font-medium">
                Password <span className="text-destructive">*</span>
              </label>
              <div className="relative">
                <Input
                  id="imd-password"
                  type={showPassword ? "text" : "password"}
                  placeholder="Min 8 characters"
                  value={password}
                  onChange={(e) => setPassword(e.target.value)}
                  className="pr-9"
                  autoComplete="new-password"
                />
                <button
                  type="button"
                  className="absolute inset-y-0 right-3 flex items-center text-muted-foreground hover:text-foreground"
                  onClick={() => setShowPassword((v) => !v)}
                  tabIndex={-1}
                >
                  {showPassword ? <EyeOff className="h-4 w-4" /> : <Eye className="h-4 w-4" />}
                </button>
              </div>
            </div>
          )}

          {/* Role */}
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
          {successMsg && <p className="text-sm text-green-600 dark:text-green-400">{successMsg}</p>}

          {/* Invite created, but no email went out — hand the link over. */}
          {handoff && (
            <div className="space-y-2 rounded-lg border border-border bg-muted/40 p-3">
              <p className="text-sm font-medium">
                Invite created — no email was sent
              </p>
              <p className="text-xs text-muted-foreground">
                {handoff.delivery_status === "not_configured"
                  ? "This instance has no email server configured. Send this link to the invitee yourself."
                  : "The invitation email could not be sent. The invite is valid — send this link to the invitee yourself."}
              </p>
              <InviteLinkBox url={handoff.invite_url} />
              {handoff.delivery_error && (
                <p className="text-xs text-muted-foreground">
                  Mail server said: {handoff.delivery_error}
                </p>
              )}
            </div>
          )}

          <DialogFooter>
            <Button type="button" variant="outline" onClick={onClose} disabled={isSubmitting}>
              {handoff ? "Done" : "Cancel"}
            </Button>
            {!handoff && (
              <Button type="submit" disabled={isSubmitting}>
                {isSubmitting ? (
                  mode === "email_link" && !selectedUser ? "Sending..." : "Adding..."
                ) : (
                  <>
                    <UserPlus className="h-4 w-4" />
                    {mode === "email_link" && !selectedUser ? "Send Invite" : "Add Member"}
                  </>
                )}
              </Button>
            )}
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
}
