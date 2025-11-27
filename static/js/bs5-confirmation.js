/**
 * Bootstrap 5 Confirmation Popover
 * A lightweight jQuery plugin for confirmation dialogs using Bootstrap 5 Popovers
 */
(function($) {
  'use strict';

  $.fn.confirmationPopover = function(options) {
    var defaults = {
      title: 'Are you sure?',
      btnOkText: 'Yes',
      btnCancelText: 'No',
      btnOkClass: 'btn-sm btn-danger',
      btnCancelClass: 'btn-sm btn-secondary',
      placement: 'left',
      onConfirm: function() {},
      onCancel: function() {}
    };

    var settings = $.extend({}, defaults, options);

    return this.each(function() {
      var $element = $(this);
      var href = $element.attr('href');
      var popoverInstance = null;

      // Prevent default action
      $element.on('click', function(e) {
        e.preventDefault();
        e.stopPropagation();

        // If popover already exists, dispose it
        if (popoverInstance) {
          popoverInstance.dispose();
          popoverInstance = null;
          return;
        }

        // Create popover content with buttons
        var content = '<div class="d-grid gap-2">' +
          '<button type="button" class="btn ' + settings.btnOkClass + ' confirm-ok">' +
            settings.btnOkText +
          '</button>' +
          '<button type="button" class="btn ' + settings.btnCancelClass + ' confirm-cancel">' +
            settings.btnCancelText +
          '</button>' +
        '</div>';

        // Initialize Bootstrap 5 popover
        popoverInstance = new bootstrap.Popover($element[0], {
          content: content,
          html: true,
          title: settings.title,
          trigger: 'manual',
          placement: settings.placement,
          sanitize: false
        });

        popoverInstance.show();

        // Handle confirm button
        $(document).one('click', '.confirm-ok', function(e) {
          e.stopPropagation();
          settings.onConfirm.call($element[0]);
          if (popoverInstance) {
            popoverInstance.dispose();
            popoverInstance = null;
          }
          // Navigate to the href
          if (href) {
            window.location.href = href;
          }
        });

        // Handle cancel button
        $(document).one('click', '.confirm-cancel', function(e) {
          e.stopPropagation();
          settings.onCancel.call($element[0]);
          if (popoverInstance) {
            popoverInstance.dispose();
            popoverInstance = null;
          }
        });

        // Close on outside click after a brief delay
        setTimeout(function() {
          $(document).one('click', function(e) {
            if (popoverInstance &&
                !$(e.target).closest('.popover').length &&
                !$(e.target).is($element)) {
              popoverInstance.dispose();
              popoverInstance = null;
            }
          });
        }, 100);
      });
    });
  };
})(jQuery);
