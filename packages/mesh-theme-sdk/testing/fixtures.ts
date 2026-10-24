import { board, column } from "./fixtures/board";
import { fields, FIELD, status, task } from "./fixtures/common";
import { doc, docNode, docTree } from "./fixtures/doc";
import * as dashboard from "./fixtures/dashboard";
import { authField, authInvite, authLogin, authRegistrationClosed, extensionItem, navItem, navProject, sidebar, topbar } from "./fixtures/nav-auth";
import { fullTask, row, rowColumns, STATUSES, view, VIEW_BLOCKS } from "./fixtures/row-view";

/** Data fixtures for `mountSlot`, one family per slot group. */
export const fixtures = { task, status, fields, FIELD, board, column, row, rowColumns, STATUSES, view, fullTask, VIEW_BLOCKS, docNode, docTree, doc, dashboard, sidebar, navItem, navProject, extensionItem, topbar, authLogin, authInvite, authRegistrationClosed, authField };
